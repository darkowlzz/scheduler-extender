/*
Copyright 2021.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"encoding/json"
	"flag"
	"net/http"
	"os"
	"strings"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"github.com/darkowlzz/operator-toolkit/webhook/cert"
	"github.com/go-logr/logr"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	schedulerapi "k8s.io/kube-scheduler/extender/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	//+kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	//+kubebuilder:scaffold:scheme
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		MetricsBindAddress:     metricsAddr,
		Port:                   9443,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "51fdb366.my.domain",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	cli, err := client.New(mgr.GetConfig(), client.Options{Scheme: mgr.GetScheme()})
	if err != nil {
		setupLog.Error(err, "failed to create raw client")
		os.Exit(1)
	}
	// Configure the certificate manager.
	certOpts := cert.Options{
		Service: &admissionregistrationv1.ServiceReference{
			Name:      "webhook-service",
			Namespace: "default",
		},
		Client:    cli,
		SecretRef: &types.NamespacedName{Name: "webhook-secret", Namespace: "default"},
	}
	if err := cert.NewManager(nil, certOpts); err != nil {
		setupLog.Error(err, "unable to provision certificate")
		os.Exit(1)
	}

	mgr.GetWebhookServer().Register("/filter", NodeFilter{Log: ctrl.Log.WithName("node-filter"), Cli: cli})
	mgr.GetWebhookServer().Register("/prioritize", NodeScorer{Log: ctrl.Log.WithName("node-scorer"), Cli: cli})

	//+kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

type NodeFilter struct {
	Log logr.Logger
	Cli client.Client
}

func (nf NodeFilter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var extenderArgs schedulerapi.ExtenderArgs
	var extenderFilterResult *schedulerapi.ExtenderFilterResult

	if err := json.NewDecoder(r.Body).Decode(&extenderArgs); err != nil {
		extenderFilterResult = &schedulerapi.ExtenderFilterResult{
			Error: err.Error(),
		}
	} else {
		extenderFilterResult = nf.filter(extenderArgs)
	}

	if response, err := json.Marshal(extenderFilterResult); err != nil {
		nf.Log.Error(err, "failed marshalling")
	} else {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(response)
	}
}

type NodeScorer struct {
	Log logr.Logger
	Cli client.Client
}

func (ns NodeScorer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var extenderArgs schedulerapi.ExtenderArgs
	var hostPriorityList *schedulerapi.HostPriorityList

	if err := json.NewDecoder(r.Body).Decode(&extenderArgs); err != nil {
		ns.Log.Error(err, "failed decoding")
		hostPriorityList = &schedulerapi.HostPriorityList{}
	} else {
		hostPriorityList = ns.prioritize(extenderArgs)
	}

	if response, err := json.Marshal(hostPriorityList); err != nil {
		ns.Log.Error(err, "failed marshalling")
	} else {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(response)
	}
}

func (nf NodeFilter) filter(args schedulerapi.ExtenderArgs) *schedulerapi.ExtenderFilterResult {
	nf.Log.Info("filter nodes", "pod", args.Pod.Name)

	// Read the configuration and filter.
	data := getConfig(nf.Log, nf.Cli, types.NamespacedName{Name: "game-demo", Namespace: "default"})
	nf.Log.Info("allowed nodes", "nodes", data["allowed"])
	allowedList := strings.Split(data["allowed"], ",")

	allNodeNames := []string{}
	filteredNodeNames := []string{}
	resultNodeList := &corev1.NodeList{}

	for _, n := range args.Nodes.Items {
		allNodeNames = append(allNodeNames, n.Name)
		if contains(allowedList, n.Name) {
			filteredNodeNames = append(filteredNodeNames, n.Name)
			resultNodeList.Items = append(resultNodeList.Items, n)
		}
	}

	nf.Log.Info("given nodes", "nodes", allNodeNames)
	nf.Log.Info("filtered nodes", "nodes", filteredNodeNames)

	return &schedulerapi.ExtenderFilterResult{
		Nodes:       resultNodeList,
		FailedNodes: schedulerapi.FailedNodesMap{},
		Error:       "",
	}
}

func (ns NodeScorer) prioritize(args schedulerapi.ExtenderArgs) *schedulerapi.HostPriorityList {
	ns.Log.Info("prioritize nodes", "pod", args.Pod.Name)

	// Read the configuration and score.
	data := getConfig(ns.Log, ns.Cli, types.NamespacedName{Name: "game-demo", Namespace: "default"})
	ns.Log.Info("target node", "node", data["target"])

	hpl := make(schedulerapi.HostPriorityList, len(args.Nodes.Items))
	for i, node := range args.Nodes.Items {
		score := int64(5)
		if node.Name == data["target"] {
			score = int64(10)
		}
		hpl[i] = schedulerapi.HostPriority{
			Host:  node.Name,
			Score: score,
		}
	}

	ns.Log.Info("priority result", "scores", hpl)

	return &hpl
}

func getConfig(log logr.Logger, cli client.Client, nsn types.NamespacedName) map[string]string {
	cm := &corev1.ConfigMap{}
	if err := cli.Get(context.TODO(), nsn, cm); err != nil {
		log.Error(err, "configmap not found")
	}
	return cm.Data
}

func contains(s []string, e string) bool {
	for _, a := range s {
		if a == e {
			return true
		}
	}
	return false
}
