package v1beta1

import esmeta "github.com/external-secrets/external-secrets/apis/meta/v1"

type BeyondTrustProviderSecretRef struct {

	// Value can be specified directly to set a value without using a secret.
	// +optional
	Value string `json:"value,omitempty"`

	// SecretRef references a key in a secret that will be used as value.
	// +optional
	SecretRef *esmeta.SecretKeySelector `json:"secretRef,omitempty"`
}

type BeyondtrustProvider struct {
	Host           string                        `json:"host"`
	Clientid       *BeyondTrustProviderSecretRef `json:"clientid"`
	Clientsecret   *BeyondTrustProviderSecretRef `json:"clientsecret"`
	Certificate    *BeyondTrustProviderSecretRef `json:"certificate"`
	Certificatekey *BeyondTrustProviderSecretRef `json:"certificatekey"`
	Retrievaltype  string                        `json:"retrievaltype"`
}
