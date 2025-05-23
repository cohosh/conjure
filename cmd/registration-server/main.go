package main

import (
	"crypto/ed25519"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/refraction-networking/conjure/pkg/metrics"
	"github.com/refraction-networking/conjure/pkg/regserver/ampCacheregserver"
	"github.com/refraction-networking/conjure/pkg/regserver/apiregserver"
	"github.com/refraction-networking/conjure/pkg/regserver/dnsregserver"
	"github.com/refraction-networking/conjure/pkg/regserver/regprocessor"
	"github.com/refraction-networking/conjure/pkg/station/lib"
	"github.com/refraction-networking/conjure/pkg/transports/connecting/dtls"
	"github.com/refraction-networking/conjure/pkg/transports/wrapping/min"
	"github.com/refraction-networking/conjure/pkg/transports/wrapping/obfs4"
	"github.com/refraction-networking/conjure/pkg/transports/wrapping/prefix"
	pb "github.com/refraction-networking/conjure/proto"
	log "github.com/sirupsen/logrus"
	"google.golang.org/protobuf/proto"
)

type regServer interface {
	ListenAndServe() error
}

// config defines the variables and options from the toml config file
type config struct {
	DNSListenAddr             string   `toml:"dns_listen_addr"`
	Domain                    string   `toml:"domain"`
	DNSPrivkeyPath            string   `toml:"dns_private_key_path"`
	APIPort                   uint16   `toml:"api_port"`
	AMPCacheURL               string   `toml:"ampcache_url"`
	ZMQAuthVerbose            bool     `toml:"zmq_auth_verbose"`
	ZMQAuthType               string   `toml:"zmq_auth_type"`
	ZMQPort                   uint16   `toml:"zmq_port"`
	ZMQBindAddr               string   `toml:"zmq_bind_addr"`
	ZMQPrivateKeyPath         string   `toml:"zmq_privkey_path"`
	StationPublicKeys         []string `toml:"station_pubkeys"`
	ClientConfPath            string   `toml:"clientconf_path"`
	latestClientConf          *pb.ClientConf
	LogLevel                  string                `toml:"log_level"`
	LogMetricsInterval        uint16                `toml:"log_metrics_interval"`
	EnforceSubnetOverrides    bool                  `toml:"enforce_subnet_overrides"`
	PrcntMinRegsToOverride    float64               `toml:"prcnt_min_regs_to_override"`
	PrcntPrefixRegsToOverride float64               `toml:"prcnt_prefix_regs_to_override"`
	OverrideSubnets           []regprocessor.Subnet `toml:"override_subnet"`
	ExclusionsFromOverride    []regprocessor.Subnet `toml:"excluded_subnet_from_overrides"`
}

var defaultTransports = map[pb.TransportType]lib.Transport{
	pb.TransportType_Min:    min.Transport{},
	pb.TransportType_Obfs4:  obfs4.Transport{},
	pb.TransportType_Prefix: prefix.DefaultSet(),
	pb.TransportType_DTLS:   dtls.Transport{},
	// [transports:enable]
}

// parseClientConf parse the latest ClientConf based on path file
func parseClientConf(path string) (*pb.ClientConf, error) {
	// Create empty client config protobuf to return in case of error
	emptyPayload := &pb.ClientConf{}

	// Check that the filepath passed in exists
	if _, err := os.Stat(path); err != nil {
		fmt.Println("filepath does not exist:", path)
		return emptyPayload, err
	}

	// Open file path that stores the client config
	in, err := os.ReadFile(path)
	if err != nil {
		fmt.Println("failed to read client config filepath:", err)
		return emptyPayload, err
	}

	// Create protobuf struct
	payload := &pb.ClientConf{}

	// Unmarshal into protobuf struct
	if err = proto.Unmarshal(in, payload); err != nil {
		fmt.Println("failed to decode protobuf body:", err)
		return emptyPayload, err
	}

	// If no error, return the payload (clientConf pb)
	return payload, nil
}

func run(regServers []regServer) {
	log.Infof("Started Conjure registration server")

	var wg sync.WaitGroup

	for _, curRegServer := range regServers {
		wg.Add(1)
		go func(regServer regServer) {
			defer wg.Done()
			err := regServer.ListenAndServe()
			if err != nil {
				log.Errorf("regServer stopped: %v", err)
			}
		}(curRegServer)
	}

	wg.Wait()
}

func readKey(path string) ([]byte, error) {
	privkey, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	} else if len(privkey) < ed25519.PrivateKeySize {
		return nil, fmt.Errorf("Private Key too short")
	}

	return privkey[:ed25519.PrivateKeySize], nil
}

// loadConfig is intended to re-parse portions of the config in conjunction with
// setupReloadHandler. This is specifically for settings where we do not want to
// restart the station. This is not intended to be a full re-build of the
// station (i.e. auth, workers, and loglevel are not changed), Mostly this
// should allow us to dynamically reload when there is an update to the latest
// client configuration or the phantom subnets that we select from.
func loadConfig(configPath string) (*config, error) {
	conf := &config{}
	_, err := toml.DecodeFile(configPath, conf)
	if err != nil {
		return nil, err
	}

	conf.latestClientConf, err = parseClientConf(conf.ClientConfPath)
	if err != nil {
		return nil, err
	}

	return conf, nil
}

func main() {
	var configPath string
	var apiOnly, ampCacheOnly, dnsOnly bool

	flag.StringVar(&configPath, "config", "", "configuration file path, alternative to CJ_REGISTRAR_CONFIG env var")
	flag.BoolVar(&apiOnly, "api-only", false, "run only the API registrar")
	flag.BoolVar(&ampCacheOnly, "ampcache-only", false, "run only the AMPCache registrar")
	flag.BoolVar(&dnsOnly, "dns-only", false, "run only the DNS registrar")
	flag.Parse()

	if configPath == "" {
		configPath = os.Getenv("CJ_REGISTRAR_CONFIG")

		if configPath == "" {
			fmt.Fprintf(os.Stderr, "configuration path is a required flag")
			flag.Usage()
			os.Exit(2)
		}
	}

	logFormatter := &log.TextFormatter{
		FullTimestamp: true,
	}

	log.SetFormatter(logFormatter)

	conf, err := loadConfig(configPath)
	if err != nil {
		log.Fatalf("error occurred while parsing config: %v", err)
	}

	logClientIP, err := strconv.ParseBool(os.Getenv("LOG_CLIENT_IP"))
	if err != nil {
		log.Errorf("failed parse client ip logging setting: %v\n", err)
		logClientIP = false
	}

	logLevel, err := log.ParseLevel(conf.LogLevel)
	if err != nil {
		log.Fatal(err)
	}
	log.SetLevel(logLevel)

	zmqPrivkey, err := readKey(conf.ZMQPrivateKeyPath)
	if err != nil {
		log.Fatal(err)
	}

	metrics := metrics.NewMetrics(log.NewEntry(log.StandardLogger()), time.Duration(conf.LogMetricsInterval)*time.Second)

	var processor *regprocessor.RegProcessor

	switch conf.ZMQAuthType {
	case "CURVE":
		processor, err = regprocessor.NewRegProcessor(conf.ZMQBindAddr, conf.ZMQPort, zmqPrivkey, conf.ZMQAuthVerbose, conf.StationPublicKeys, metrics, conf.EnforceSubnetOverrides, conf.OverrideSubnets, conf.ExclusionsFromOverride, conf.PrcntMinRegsToOverride, conf.PrcntPrefixRegsToOverride)
	case "NULL":
		processor, err = regprocessor.NewRegProcessorNoAuth(conf.ZMQBindAddr, conf.ZMQPort, metrics, conf.EnforceSubnetOverrides, conf.OverrideSubnets, conf.ExclusionsFromOverride, conf.PrcntMinRegsToOverride, conf.PrcntPrefixRegsToOverride)
	default:
		log.Fatalf("Unknown ZMQ auth type: %s", conf.ZMQAuthType)
	}

	if err != nil {
		log.Fatal(err)
	}

	for transportType, t := range defaultTransports {
		err := processor.AddTransport(transportType, t)
		if err != nil {
			log.Fatalf("failed to add transport: %s - %d", t.Name(), transportType)
		}
	}

	regServers := []regServer{}
	var dnsRegServer *dnsregserver.DNSRegServer
	var ampCacheRegServer *ampCacheregserver.AMPCacheRegServer
	var apiRegServer *apiregserver.APIRegServer

	if !apiOnly && !ampCacheOnly {
		dnsPrivKey, err := readKey(conf.DNSPrivkeyPath)
		if err != nil {
			log.Fatal(err)
		}

		dnsRegServer, err = dnsregserver.NewDNSRegServer(conf.Domain, conf.DNSListenAddr, dnsPrivKey[:32], processor, conf.latestClientConf.GetGeneration(), log.WithField("registrar", "DNS"), metrics)
		if err != nil {
			log.Fatal(err)
		}

		regServers = append(regServers, dnsRegServer)
	}

	if !dnsOnly && !ampCacheOnly {
		apiRegServer, err = apiregserver.NewAPIRegServer(conf.APIPort, processor, conf.latestClientConf, log.WithField("registrar", "API"), logClientIP, metrics)
		if err != nil {
			log.Fatal(err)
		}

		regServers = append(regServers, apiRegServer)
	}
	if !dnsOnly && !apiOnly {
		ampCacheRegServer, err = ampCacheregserver.NewAMPCacheRegServer(conf.APIPort, conf.AMPCacheURL, processor, conf.latestClientConf, log.WithField("registrar", "AMPCache"), logClientIP, metrics)
		if err != nil {
			log.Fatal(err)
		}
		regServers = append(regServers, ampCacheRegServer)
	}

	signalChan := make(chan os.Signal, 1)

	signal.Notify(
		signalChan,
		syscall.SIGHUP, // listen for SIGHUP as reload signal
	)

	// spawn a goroutine to handle os signals continuously
	go func() {
		for {
			sig := <-signalChan

			if sig == syscall.SIGHUP {
				conf, err = loadConfig(configPath)
				if err != nil {
					log.Errorf("error occurred while reloading config -- aborting reload: %v", err)
				} else {
					err := processor.ReloadSubnets()
					if err != nil {
						log.Errorf("failed to reload phantom subnets - aborting reload: %v", err)
					}
					if apiRegServer != nil {
						apiRegServer.NewClientConf(conf.latestClientConf)
					}

					if dnsRegServer != nil {
						dnsRegServer.UpdateLatestCCGen(conf.latestClientConf.GetGeneration())
					}
					if ampCacheRegServer != nil {
						ampCacheRegServer.NewClientConf(conf.latestClientConf)
					}
				}
			}
		}
	}()

	run(regServers)
}
