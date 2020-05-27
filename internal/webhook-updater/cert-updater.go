package webhook_updater

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
	"k8s.io/apimachinery/pkg/api/validation"
)

const retryInterval = time.Minute

type CertUpdater struct {
	Webhook string `json:"webhook"`
	CaDir   string `json:"ca_dir"`
	CaFile  string `json:"ca_file"`

	cachedCa []byte
	done chan struct{}
}

func (updater *CertUpdater) Validate() error {
	if updater.Webhook == "" {
		return errors.New("webhook parameter cannot be empty")
	}
	if !isValidK8sResourceName(updater.Webhook) {
		return errors.New("webhook parameter must be a valid Kubernetes resource name")
	}

	dirStat, err := os.Stat(updater.CaDir)
	if err != nil {
		return err
	}
	if !dirStat.IsDir() {
		return fmt.Errorf("'%s' has to be a directory", updater.CaDir)
	}

	if updater.CaFile == "" {
		return errors.New("ca_file parameter has to be specified")
	}

	return nil
}

func isValidK8sResourceName(name string) bool {
	nameErrors := validation.NameIsDNSSubdomain(name, false)
	return len(nameErrors) == 0
}

func (updater *CertUpdater) Start(client ClientProxy) {
	updater.cachedCa = nil
	updater.done = make(chan struct{})

	// TODO -- where to handle this check?
	if !client.CheckWebhookExists(updater.Webhook) {
		Log.Errorf("Webhook %s does not exist", updater.Webhook)
		// TODO -- how to handle error?
	}


	err := updater.processFileChanges(client)
	if err != nil {
		Log.Errorf("Error watching for certificates: %s", err)
		// TODO -- how to handle error?
	}
}

func (updater *CertUpdater) Stop() {
	// TODO -- cannot close channel twice !!!
	close(updater.done)
}



func (updater *CertUpdater) processFileChanges(client ClientProxy) error {
	fileChanged, closer, err := watchFsChanges(updater.CaDir)
	if err != nil {
		return err
	}
	defer closer.Close()

	notify(fileChanged)
	for {
		select {
		case _, ok := <-fileChanged:
			if !ok {
				return nil
			}

			err := updater.refreshWebhook(client)
			if err != nil {
				// If the webhook does not exist, exit
				if err == WebhookNotFoundError {
					return err
				}

				go func() {
					time.Sleep(retryInterval)
					notify(fileChanged)
				}()
			}
		case <-updater.done:
			return nil
		}
	}
}

func watchFsChanges(directory string) (fileChanged chan struct{}, closer io.Closer, err error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		Log.Errorf("Failed to create an inotify watcher. Reason: %s", err)
		return nil, nil, err
	}

	err = watcher.Add(directory)
	if err != nil {
		watcher.Close()
		Log.Errorf("Failed to establish a watch on %s. Reason: %s", directory, err)
		return nil, nil, err
	}

	filesChanged := make(chan struct{}, 1)
	go func() {
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if event.Op != fsnotify.Chmod && event.Op != fsnotify.Remove {
					notify(filesChanged)
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				Log.Errorf("An error occurred when watching %s. Reason: %s", directory, err)
			}
		}
	}()

	return filesChanged, watcher, nil
}

func notify(channel chan struct{}) {
	select {
	case channel <- struct{}{}:
	default:
	}
}

func (updater *CertUpdater) refreshWebhook(client ClientProxy) error {
	fileName := filepath.Join(updater.CaDir, updater.CaFile)
	certBytes, err := ioutil.ReadFile(fileName)
	if err != nil {
		Log.Errorf("Failed to read file: %s", err)
		return err
	}

	if bytes.Equal(certBytes, updater.cachedCa) {
		// The CA did not change, no need to call K8S API
		return nil
	}

	err = validateCaBundle(certBytes)
	if err != nil {
		Log.Errorf("The CA bundle is not valid: %s", err)
		return err
	}

	err = client.PatchWebhookCaBundle(updater.Webhook, certBytes)
	if err != nil {
		Log.Errorf("Failed to patch webhook: %s", err)
		return err
	}

	updater.cachedCa = certBytes
	return nil
}

func validateCaBundle(caBundle []byte) error {
	data := caBundle
	blockCount := 0
	for {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			break
		}

		blockCount += 1
		if block.Type != "CERTIFICATE" {
			return fmt.Errorf("CA bundle contains block type: %s", block.Type)
		}

		_, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return err
		}
	}

	if blockCount == 0 {
		return errors.New("file is not in valid PEM format")
	}
	return nil
}