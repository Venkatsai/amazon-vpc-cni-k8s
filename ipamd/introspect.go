// Copyright 2014-2017 Amazon.com, Inc. or its affiliates. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"). You may
// not use this file except in compliance with the License. A copy of the
// License is located at
//
//      http://aws.amazon.com/apache2.0/
//
// or in the "license" file accompanying this file. This file is distributed
// on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either
// express or implied. See the License for the specific language governing
// permissions and limitations under the License.

package ipamd

import (
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/aws/amazon-vpc-cni-k8s/pkg/networkutils"
	"github.com/aws/amazon-vpc-cni-k8s/pkg/utils"
	log "github.com/cihub/seelog"
)

const (
	// introspectionAddress is listening on localhost 61679 for ipamd introspection
	introspectionAddress = "127.0.0.1:61679"

	// Environment variable to disable the introspection endpoints
	envDisableIntrospection = "DISABLE_INTROSPECTION"
)

type rootResponse struct {
	AvailableCommands []string
}

// LoggingHandler is a object for handling http request
type LoggingHandler struct {
	h http.Handler
}

func (lh LoggingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	log.Info("Handling http request: ", ", method: ", r.Method, ", from: ", r.RemoteAddr, ", URI: ", r.RequestURI)
	lh.h.ServeHTTP(w, r)
}

// ServeIntrospection sets up ipamd introspection endpoints
func (c *IPAMContext) ServeIntrospection() {
	if disableIntrospection() {
		log.Info("Introspection endpoints disabled")
		return
	}

	log.Info("Serving introspection endpoints on ", introspectionAddress)
	server := c.setupIntrospectionServer()
	for {
		once := sync.Once{}
		_ = utils.RetryWithBackoff(utils.NewSimpleBackoff(time.Second, time.Minute, 0.2, 2), func() error {
			err := server.ListenAndServe()
			once.Do(func() {
				log.Error("Error running http API: ", err)
			})
			return err
		})
	}
}

func (c *IPAMContext) setupIntrospectionServer() *http.Server {
	// If enabled, add introspection endpoints
	serverFunctions := map[string]func(w http.ResponseWriter, r *http.Request){
		"/v1/enis":                      eniV1RequestHandler(c),
		"/v1/eni-configs":               eniConfigRequestHandler(c),
		"/v1/pods":                      podV1RequestHandler(c),
		"/v1/networkutils-env-settings": networkEnvV1RequestHandler(),
		"/v1/ipamd-env-settings":        ipamdEnvV1RequestHandler(),
	}
	paths := make([]string, 0, len(serverFunctions))
	for path := range serverFunctions {
		paths = append(paths, path)
	}
	availableCommands := &rootResponse{paths}
	// Autogenerated list of the above serverFunctions paths
	availableCommandResponse, err := json.Marshal(&availableCommands)

	if err != nil {
		log.Error("Failed to marshal: %v", err)
	}

	defaultHandler := func(w http.ResponseWriter, r *http.Request) {
		logErr(w.Write(availableCommandResponse))
	}
	serveMux := http.NewServeMux()
	serveMux.HandleFunc("/", defaultHandler)
	for key, fn := range serverFunctions {
		serveMux.HandleFunc(key, fn)
	}

	// Log all requests and then pass through to serveMux
	loggingServeMux := http.NewServeMux()
	loggingServeMux.Handle("/", LoggingHandler{serveMux})

	server := &http.Server{
		Addr:         introspectionAddress,
		Handler:      loggingServeMux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}
	return server
}

func eniV1RequestHandler(ipam *IPAMContext) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		responseJSON, err := json.Marshal(ipam.dataStore.GetENIInfos())
		if err != nil {
			log.Errorf("Failed to marshal ENI data: %v", err)
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		logErr(w.Write(responseJSON))
	}
}

func podV1RequestHandler(ipam *IPAMContext) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		responseJSON, err := json.Marshal(ipam.dataStore.GetPodInfos())
		if err != nil {
			log.Errorf("Failed to marshal pod data: %v", err)
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		logErr(w.Write(responseJSON))
	}
}

func eniConfigRequestHandler(ipam *IPAMContext) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		responseJSON, err := json.Marshal(ipam.eniConfig.Getter())
		if err != nil {
			log.Errorf("Failed to marshal ENI config: %v", err)
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		logErr(w.Write(responseJSON))
	}
}

func networkEnvV1RequestHandler() func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		responseJSON, err := json.Marshal(networkutils.GetConfigForDebug())
		if err != nil {
			log.Errorf("Failed to marshal network env var data: %v", err)
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		logErr(w.Write(responseJSON))
	}
}

func ipamdEnvV1RequestHandler() func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		responseJSON, err := json.Marshal(GetConfigForDebug())
		if err != nil {
			log.Errorf("Failed to marshal ipamd env var data: %v", err)
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		logErr(w.Write(responseJSON))
	}
}

func logErr(_ int, err error) {
	if err != nil {
		log.Errorf("Write failed: %v", err)
	}
}

// disableIntrospection returns true if we should disable the introspection
func disableIntrospection() bool {
	return getEnvBoolWithDefault(envDisableIntrospection, false)
}

func getEnvBoolWithDefault(envName string, def bool) bool {
	if strValue := os.Getenv(envName); strValue != "" {
		parsedValue, err := strconv.ParseBool(strValue)
		if err == nil {
			return parsedValue
		}
		log.Errorf("Failed to parse %s, using default `%t`: %v", envName, def, err.Error())
	}
	return def
}
