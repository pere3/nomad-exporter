package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/nomad/api"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/version"
)

const (
	namespace = "nomad"
)

var (
	up = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "", "up"),
		"Was the last query of Nomad successful.",
		nil, nil,
	)
	allocationMemoryLimit = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "", "allocation_memory_limit"),
		"Allocation memory limit",
		[]string{"job", "group", "alloc", "alloc_id", "region", "datacenter", "node"}, nil,
	)
)

func AllocationsByStatus(allocs []*api.AllocationListStub, status string) []*api.AllocationListStub {
	var resp []*api.AllocationListStub
	for _, a := range allocs {
		if a.ClientStatus == status {
			resp = append(resp, a)
		}
	}
	return resp
}

type Exporter struct {
	client *api.Client
}

func NewExporter(cfg *api.Config) (*Exporter, error) {
	client, err := api.NewClient(cfg)
	if err != nil {
		return nil, err
	}
	return &Exporter{
		client: client,
	}, nil
}

// Describe implements Collector interface.
func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	ch <- up
	ch <- allocationMemoryLimit
}

// Collect collects nomad metrics
func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	ch <- prometheus.MustNewConstMetric(
		up, prometheus.GaugeValue, 1,
	)
	allocs, _, err := e.client.Allocations().List(&api.QueryOptions{})
	if err != nil {
		logError(err)
		return
	}

	runningAllocs := AllocationsByStatus(allocs, "running")

	var w sync.WaitGroup
	for _, a := range runningAllocs {
		w.Add(1)
		go func(a *api.AllocationListStub) {
			defer w.Done()
			alloc, _, err := e.client.Allocations().Info(a.ID, &api.QueryOptions{})
			if err != nil {
				logError(err)
				return
			}
			
			node, _, err := e.client.Nodes().Info(alloc.NodeID, &api.QueryOptions{})
			if err != nil {
				logError(err)
				return
			}
			ch <- prometheus.MustNewConstMetric(
				allocationMemoryLimit, prometheus.GaugeValue, float64(alloc.Resources.MemoryMB), alloc.Job.Name, alloc.TaskGroup, alloc.Name, alloc.ID, alloc.Job.Region, node.Datacenter, node.Name,
			)
		}(a)
	}
	w.Wait()
}

func getRunningAllocs(client *api.Client, nodeID string) ([]*api.Allocation, error) {
	var allocs []*api.Allocation

	// Query the node allocations
	nodeAllocs, _, err := client.Nodes().Allocations(nodeID, nil)
	// Filter list to only running allocations
	for _, alloc := range nodeAllocs {
		if alloc.ClientStatus == "running" {
			allocs = append(allocs, alloc)
		}
	}
	return allocs, err
}

func main() {
	var (
		showVersion   = flag.Bool("version", false, "Print version information.")
		listenAddress = flag.String("web.listen-address", ":9172", "Address to listen on for web interface and telemetry.")
		metricsPath   = flag.String("web.telemetry-path", "/metrics", "Path under which to expose metrics.")
		nomadServer   = flag.String("nomad.server", "http://localhost:4646", "HTTP API address of a Nomad server or agent.")
		nomadTimeout  = flag.String("nomad.timeout", "30", "HTTP timeout to contact Nomad agent.")
		tlsCaFile     = flag.String("tls.ca-file", "", "ca-file path to a PEM-encoded CA cert file to use to verify the connection to nomad server")
		tlsCaPath     = flag.String("tls.ca-path", "", "ca-path is the path to a directory of PEM-encoded CA cert files to verify the connection to nomad server")
		tlsCert       = flag.String("tls.cert-file", "", "cert-file is the path to the client certificate for Nomad communication")
		tlsKey        = flag.String("tls.key-file", "", "key-file is the path to the key for cert-file")
		tlsInsecure   = flag.Bool("tls.insecure", false, "insecure enables or disables SSL verification")
		tlsServerName = flag.String("tls.tls-server-name", "", "tls-server-name sets the SNI for Nomad ssl connection")
	)
	flag.Parse()

	if *showVersion {
		fmt.Fprintln(os.Stdout, version.Print("nomad_exporter"))
		os.Exit(0)
	}
	cfg := api.DefaultConfig()
	cfg.Address = *nomadServer

	if strings.HasPrefix(cfg.Address, "https://") {
		cfg.TLSConfig.CACert = *tlsCaFile
		cfg.TLSConfig.CAPath = *tlsCaPath
		cfg.TLSConfig.ClientKey = *tlsKey
		cfg.TLSConfig.ClientCert = *tlsCert
		cfg.TLSConfig.Insecure = *tlsInsecure
		cfg.TLSConfig.TLSServerName = *tlsServerName
	}

	timeout, err := strconv.Atoi(*nomadTimeout)
	if err != nil {
		log.Fatal(err)
	}
	cfg.WaitTime = time.Duration(timeout) * time.Second

	exporter, err := NewExporter(cfg)
	if err != nil {
		log.Fatal(err)
	}
	prometheus.MustRegister(exporter)

	http.Handle(*metricsPath, prometheus.Handler())
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html>
             <head><title>Nomad Exporter</title></head>
             <body>
             <h1>Nomad Exporter</h1>
             <p><a href='` + *metricsPath + `'>Metrics</a></p>
             </body>
             </html>`))
	})

	log.Println("Listening on", *listenAddress)
	log.Fatal(http.ListenAndServe(*listenAddress, nil))
}

func logError(err error) {
	log.Println("Query error", err)
	return
}
