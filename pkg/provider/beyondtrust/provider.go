package beyondtrust

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	esv1beta1 "github.com/external-secrets/external-secrets/apis/externalsecrets/v1beta1"
	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	// Invoking Secret Safe Library
	"github.com/btfhernandez/go-client-library-passwordsafe/api/authentication"
	"github.com/btfhernandez/go-client-library-passwordsafe/api/logging"
	managed_account "github.com/btfhernandez/go-client-library-passwordsafe/api/managed_account"
	"github.com/btfhernandez/go-client-library-passwordsafe/api/secrets"
	"github.com/btfhernandez/go-client-library-passwordsafe/api/utils"

	backoff "github.com/cenkalti/backoff/v4"
	"go.uber.org/zap"

	corev1 "k8s.io/api/core/v1"
	kubeClient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	errNilStore         = "nil store found"
	errMissingStoreSpec = "store is missing spec"
	errMissingProvider  = "storeSpec is missing provider"
	errInvalidProvider  = "Invalid provider spec. Missing field in store %s"
	errInvalidHostURL   = "Ivalid host URL"
	errNoSuchKeyFmt     = "no such key in secret: %q"
)

var (
	errSecretRefAndValueConflict = errors.New("cannot specify both secret reference and value")
	errMissingSecretName         = errors.New("must specify a secret name")
	errMissingSecretKey          = errors.New("must specify a secret key")
	errSecretRefAndValueMissing  = errors.New("must specify either secret reference or direct value")
)

// this struct will hold the keys that the service returns
type keyValue struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type Provider struct {
	config         *esv1beta1.BeyondtrustProvider
	hostUrl        string
	clientId       string
	clientSecret   string
	retrievaltype  string
	certificate    string
	certificatekey string
}

type GetTokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope"`
}

type SecretResponseAPI struct {
	Password string `json:"Password"`
	Id       string `json:"Id"`
	Title    string `json:"Title"`
}

// Capabilities implements v1beta1.Provider.
func (*Provider) Capabilities() esv1beta1.SecretStoreCapabilities {
	return esv1beta1.SecretStoreReadOnly
}

// Close implements v1beta1.SecretsClient.
func (*Provider) Close(ctx context.Context) error {
	return nil
}

// DeleteSecret implements v1beta1.SecretsClient.
func (*Provider) DeleteSecret(ctx context.Context, remoteRef esv1beta1.PushSecretRemoteRef) error {
	panic("unimplemented")
}

// GetSecretMap implements v1beta1.SecretsClient.
func (*Provider) GetSecretMap(ctx context.Context, ref esv1beta1.ExternalSecretDataRemoteRef) (map[string][]byte, error) {
	panic("unimplemented")
}

// PushSecret implements v1beta1.SecretsClient.
func (*Provider) PushSecret(ctx context.Context, secret *v1.Secret, data esv1beta1.PushSecretData) error {
	panic("unimplemented")
}

// Validate implements v1beta1.SecretsClient.
func (*Provider) Validate() (esv1beta1.ValidationResult, error) {
	return esv1beta1.ValidationResultError, nil
}

func (*Provider) SecretExists(_ context.Context, _ esv1beta1.PushSecretRemoteRef) (bool, error) {
	panic("unimplemented")
}

// NewClient this is where we initialize the SecretClient and return it for the controller to use
func (p *Provider) NewClient(ctx context.Context, store esv1beta1.GenericStore, kube client.Client, namespace string) (esv1beta1.SecretsClient, error) {
	fmt.Println("Getting Client..............")

	config := store.GetSpec().Provider.Beyondtrust

	clientId, err := loadConfigSecret(ctx, config.Clientid, kube, namespace)
	if err != nil {
		return nil, err
	}

	clientSecret, err := loadConfigSecret(ctx, config.Clientsecret, kube, namespace)
	if err != nil {
		return nil, err
	}

	certificate, err := loadConfigSecret(ctx, config.Certificate, kube, namespace)
	if err != nil {
		return nil, err
	}

	certificateKey, err := loadConfigSecret(ctx, config.Certificatekey, kube, namespace)
	if err != nil {
		return nil, err
	}

	return &Provider{
		config:         config,
		hostUrl:        config.Host,
		clientId:       clientId,
		clientSecret:   clientSecret,
		retrievaltype:  config.Retrievaltype,
		certificate:    certificate,
		certificatekey: certificateKey,
	}, nil
}

func loadConfigSecret(ctx context.Context, ref *esv1beta1.BeyondTrustProviderSecretRef, kube kubeClient.Client, defaultNamespace string) (string, error) {
	if ref.SecretRef == nil {
		return ref.Value, nil
	}

	if err := validateSecretRef(ref); err != nil {
		return "", err
	}

	namespace := defaultNamespace
	if ref.SecretRef.Namespace != nil {
		namespace = *ref.SecretRef.Namespace
	}

	objKey := kubeClient.ObjectKey{Namespace: namespace, Name: ref.SecretRef.Name}
	secret := corev1.Secret{}
	err := kube.Get(ctx, objKey, &secret)
	if err != nil {
		return "", err
	}

	value, ok := secret.Data[ref.SecretRef.Key]
	if !ok {
		return "", fmt.Errorf(errNoSuchKeyFmt, ref.SecretRef.Key)
	}

	return string(value), nil
}

func validateSecretRef(ref *esv1beta1.BeyondTrustProviderSecretRef) error {
	if ref.SecretRef != nil {
		if ref.Value != "" {
			return errSecretRefAndValueConflict
		}
		if ref.SecretRef.Name == "" {
			return errMissingSecretName
		}
		if ref.SecretRef.Key == "" {
			return errMissingSecretKey
		}
	} else if ref.Value == "" {
		return errSecretRefAndValueMissing
	}

	return nil
}

func (p *Provider) GetAllSecrets(ctx context.Context, ref esv1beta1.ExternalSecretFind) (map[string][]byte, error) {
	return nil, fmt.Errorf("GetAllSecrets not implemented")
}

// GetSecret reads the secret from the Express server and returns it. The controller uses the value here to
// create the Kubernetes secret
func (p *Provider) GetSecret(ctx context.Context, ref esv1beta1.ExternalSecretDataRemoteRef) ([]byte, error) {

	logger, _ := zap.NewDevelopment()
	zapLogger := logging.NewZapLogger(logger)

	secretData := strings.Split(ref.Key, "/")

	apiUrl := p.hostUrl
	clientId := p.clientId
	clientSecret := p.clientSecret
	separator := "/"
	certificate := ""
	certificateKey := ""
	clientTimeOutInSeconds := 5
	verifyCa := true
	retryMaxElapsedTimeMinutes := 15
	maxFileSecretSizeBytes := 5000000

	backoffDefinition := backoff.NewExponentialBackOff()
	backoffDefinition.InitialInterval = 1 * time.Second
	backoffDefinition.MaxElapsedTime = time.Duration(retryMaxElapsedTimeMinutes) * time.Second
	backoffDefinition.RandomizationFactor = 0.5

	// validate inputs
	errorsInInputs := utils.ValidateInputs(clientId, clientSecret, &apiUrl, clientTimeOutInSeconds, &separator, verifyCa, zapLogger, certificate, certificateKey, &retryMaxElapsedTimeMinutes, &maxFileSecretSizeBytes)

	if errorsInInputs != nil {
		return nil, fmt.Errorf("Error: %s", errorsInInputs)
	}

	// creating a http client
	httpClientObj, _ := utils.GetHttpClient(clientTimeOutInSeconds, verifyCa, certificate, certificateKey, zapLogger)

	// instantiating authenticate obj, injecting httpClient object
	authenticate, _ := authentication.Authenticate(*httpClientObj, backoffDefinition, apiUrl, clientId, clientSecret, zapLogger, retryMaxElapsedTimeMinutes)

	// authenticating
	_, err := authenticate.GetPasswordSafeAuthentication()
	if err != nil {
		return nil, fmt.Errorf("Error: %s", err)
	}

	var returnSecret string
	secret := keyValue{
		Key:   "secret",
		Value: "",
	}

	if p.retrievaltype == "SECRET" {
		secretObj, _ := secrets.NewSecretObj(*authenticate, zapLogger, maxFileSecretSizeBytes)
		returnSecret, _ = secretObj.GetSecret(secretData[0]+"/"+secretData[1], separator)
		secret.Value = returnSecret

	} else if p.retrievaltype == "MANAGED_ACCOUNT" {
		manageAccountObj, _ := managed_account.NewManagedAccountObj(*authenticate, zapLogger)
		returnSecret, _ := manageAccountObj.GetSecret(secretData[0]+"/"+secretData[1], separator)
		secret.Value = returnSecret
	} else {
		return nil, fmt.Errorf("unsupported Retrieval Type: %s", p.retrievaltype)
	}

	return []byte(secret.Value), nil

}

// ValidateStore validates the store configuration to prevent unexpected errors
func (p *Provider) ValidateStore(store esv1beta1.GenericStore) (admission.Warnings, error) {
	if store == nil {
		return nil, fmt.Errorf(errNilStore)
	}

	spec := store.GetSpec()

	if spec == nil {
		return nil, fmt.Errorf(errMissingStoreSpec)
	}

	if spec.Provider == nil {
		return nil, fmt.Errorf(errMissingProvider)
	}

	provider := spec.Provider.Beyondtrust
	if provider == nil {
		return nil, fmt.Errorf(errInvalidProvider, store.GetObjectMeta().String())
	}

	hostUrl, err := url.Parse(provider.Host)
	if err != nil {
		return nil, fmt.Errorf(errInvalidHostURL)
	}

	if hostUrl.Host == "" {
		return nil, fmt.Errorf(errInvalidHostURL)
	}

	return nil, nil
}

// registers the provider object to process on each reconciliation loop
func init() {
	fmt.Println("Starting Porvider......")
	esv1beta1.Register(&Provider{}, &esv1beta1.SecretStoreProvider{
		Beyondtrust: &esv1beta1.BeyondtrustProvider{},
	})
}
