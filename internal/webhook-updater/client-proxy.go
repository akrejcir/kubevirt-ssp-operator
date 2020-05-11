package webhook_updater

import (
	"encoding/base64"
	"errors"
	"fmt"
	"k8s.io/client-go/rest"
	"net/http"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

type ClientProxy interface {
	CheckWebhookExists(name string) bool
	PatchWebhookCaBundle(name string, caBundle []byte) error
}

var WebhookNotFoundError = errors.New("webhook not found")

func NewClientProxy(kubeconfig string) (ClientProxy, error) {
	var config *rest.Config
	var err error
	if kubeconfig == "" {
		config, err = rest.InClusterConfig()
	} else {
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	}

	if err != nil {
		return nil, err
	}

	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	return &clientProxy{client: client}, nil
}

type clientProxy struct {
	client kubernetes.Interface
}

func (c *clientProxy) CheckWebhookExists(name string) bool {
	_, err := c.client.AdmissionregistrationV1beta1().
		ValidatingWebhookConfigurations().
		Get(name, metav1.GetOptions{})
	return err == nil
}

func (c *clientProxy) PatchWebhookCaBundle(name string, caBundle []byte) error {
	caBundleEncoded := base64.StdEncoding.EncodeToString(caBundle)

	patch := fmt.Sprintf(`[{"op":"replace", "path":"/webhooks/0/clientConfig/caBundle", "value":"%s"}]`, caBundleEncoded)
	_, err := c.client.AdmissionregistrationV1beta1().
		ValidatingWebhookConfigurations().
		Patch(name, types.JSONPatchType, []byte(patch))

	if err != nil {
		// TODO -- test for other errors? connection issue, auth issue ?
		if err.(*apierrors.StatusError).ErrStatus.Code == http.StatusNotFound {
			return WebhookNotFoundError
		}
		return err
	}
	return nil
}
