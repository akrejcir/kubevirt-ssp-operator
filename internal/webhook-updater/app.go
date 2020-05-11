package webhook_updater

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	flag "github.com/spf13/pflag"
)

type App struct {
	kubeconfig string
	port       int

	client      ClientProxy
	certUpdater *CertUpdater
}

func (app *App) Run() {
	err := app.parseAndValidateFlags()
	if err != nil {
		// TODO -- return reasonable error
		panic(err)
	}

	app.client, err = NewClientProxy(app.kubeconfig)
	if err != nil {
		Log.Errorf("Error creating kubernetes client: %s", err)
		os.Exit(1)
	}

	http.HandleFunc("/", app.handleRequest)
	server := &http.Server{Addr: fmt.Sprintf(":%d", app.port)}

	registerSignalHandler(func() { server.Close() })
	defer func() {
		// TODO -- verify race
		if app.certUpdater != nil {
			app.certUpdater.Stop()
		}
	}()

	err = server.ListenAndServe()
	if err != nil && err != http.ErrServerClosed {
		Log.Errorf("Server listen failed: %s", err)
		os.Exit(1)
	}
}

func (app *App) parseAndValidateFlags() error {
	// TODO -- add glog options for kubernetes API
	flag.IntVarP(&app.port, "port", "p", 80, "port on which the process listens to commands (default 80)")
	flag.StringVarP(&app.kubeconfig, "kubeconfig", "c", "", "absolute path to the kubeconfig file")
	flag.Parse()

	if app.kubeconfig != "" {
		stat, err := os.Stat(app.kubeconfig)
		if err != nil {
			return err
		}
		if stat.IsDir() {
			return errors.New("kubeconfig does not point to a file")
		}
	}

	return nil
}

func registerSignalHandler(handler func()) {
	signalChannel := make(chan os.Signal, 1)
	signal.Notify(signalChannel, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-signalChannel
		handler()
	}()
}

func (app *App) handleRequest(writer http.ResponseWriter, request *http.Request) {
	if request.Method != "POST" {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	err := app.handleCommand(request.Body)
	if err != nil {
		// TODO - handle error
		//  - post it back, so client from ansible knows why it failed?
		writer.WriteHeader(http.StatusBadRequest)
		Log.Infof("Bad request: %s", err)
	} else {
		writer.WriteHeader(http.StatusOK)
	}
}

func (app *App) handleCommand(body io.Reader) error {
	decoder := json.NewDecoder(body)
	certUpdater := &CertUpdater{}
	err := decoder.Decode(&certUpdater)
	if err != nil {
		return err
	}

	err = certUpdater.Validate()
	if err != nil {
		return err
	}

	if app.certUpdater != nil {
		// TODO -- what if it is not running ?
		app.certUpdater.Stop()
		app.certUpdater = nil
	}

	app.certUpdater = certUpdater
	// TODO -- how to handle errors ?
	app.certUpdater.Start(app.client)

	return nil
}
