package registration

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/refraction-networking/conjure/pkg/registrars/lib"
	pb "github.com/refraction-networking/conjure/proto"
	"github.com/refraction-networking/gotapdance/tapdance"
	"github.com/sirupsen/logrus"
	"google.golang.org/protobuf/proto"
	/*
		"gitlab.torproject.org/tpo/anti-censorship/pluggable-transports/snowflake/v2/common/amp"
		"gitlab.torproject.org/tpo/anti-censorship/pluggable-transports/snowflake/v2/common/messages"
		"gitlab.torproject.org/tpo/anti-censorship/pluggable-transports/snowflake/v2/common/util"

	*/)

type AMPCacheRegistrar struct {
	// endpoint to use in registration request
	endpoint    string
	ampCacheURL string
	// HTTP client to use in request
	client          *http.Client
	maxRetries      int
	connectionDelay time.Duration
	bidirectional   bool
	ip              []byte
	logger          logrus.FieldLogger
}

/*func createRequester(config *Config) (*requester.Requester, error) {
	switch config.AMPCacheTransportMethod {
	case UDP:
		return requester.NewRequester(&requester.Config{
			TransportMethod: requester.UDP,
			Target:          config.Target,
			BaseDomain:      config.BaseDomain,
			Pubkey:          config.Pubkey,
		})
	case DoT:
		return requester.NewRequester(&requester.Config{
			TransportMethod:  requester.DoT,
			UtlsDistribution: config.UTLSDistribution,
			Target:           config.Target,
			BaseDomain:       config.BaseDomain,
			Pubkey:           config.Pubkey,
		})
	case DoH:
		return requester.NewRequester(&requester.Config{
			TransportMethod:  requester.DoH,
			UtlsDistribution: config.UTLSDistribution,
			Target:           config.Target,
			BaseDomain:       config.BaseDomain,
			Pubkey:           config.Pubkey,
		})
	}

	return nil, fmt.Errorf("invalid AMPCache transport method")
}
*/

// NewAMPCacheRegistrar creates a AMPCacheRegistrar from config
func NewAMPCacheRegistrar(config *Config) (*AMPCacheRegistrar, error) {
	/*req, err := createRequester(config)
	if err != nil {
		return nil, fmt.Errorf("error creating requester: %v", err)
	}*/

	ip, err := getPublicIp(config.STUNAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to get public IP: %v", err)
	}

	return &AMPCacheRegistrar{
		endpoint:        config.Target,
		ampCacheURL:     config.AMPCacheURL,
		ip:              ip,
		maxRetries:      config.MaxRetries,
		bidirectional:   config.Bidirectional,
		connectionDelay: config.Delay,
		logger:          tapdance.Logger().WithField("registrar", "AMPCache"),
	}, nil
}

// registerBidirectional sends bidirectional registration data to the registration server and reads the response
func (r *AMPCacheRegistrar) registerBidirectional(ctx context.Context, cjSession *tapdance.ConjureSession) (*tapdance.ConjureReg, error) {
	logger := r.logger.WithFields(logrus.Fields{"type": "ampcache-bidirectional", "sessionID": cjSession.IDString()})

	reg, protoPayload, err := cjSession.BidirectionalRegData(ctx, pb.RegistrationSource_BidirectionalAMPCache.Enum())
	if err != nil {
		logger.Errorf("Failed to prepare registration data: %v", err)
		return nil, lib.ErrRegFailed
	}
	/*
		if reg.Dialer != nil {
			err := r.req.SetDialer(reg.Dialer)
			if err != nil {
				return nil, fmt.Errorf("failed to set dialer to requester: %v", err)
			}
		}
	*/

	protoPayload.RegistrationAddress = r.ip

	payload, err := proto.Marshal(protoPayload)
	if err != nil {
		logger.Errorf("failed to marshal ClientToStation payload: %v", err)
		return nil, lib.ErrRegFailed
	}

	logger.Debugf("AMPCache payload length: %d", len(payload))

	for i := 0; i < r.maxRetries+1; i++ {
		logger := logger.WithField("attempt", strconv.Itoa(i+1)+"/"+strconv.Itoa(r.maxRetries))

		bdResponse, err := r.AmpCacheRequestAndRecv(payload)
		if err != nil {
			logger.Warnf("error in sending request to AMPCache registrar: %v", err)
			continue
		}

		dnsResp := &pb.DnsResponse{}
		err = proto.Unmarshal(bdResponse, dnsResp)
		if err != nil {
			logger.Warnf("error in storing Registrtion Response protobuf: %v", err)
			continue
		}
		if !dnsResp.GetSuccess() {
			logger.Warnf("registrar indicates that registration failed")
			continue
		}
		if dnsResp.GetClientconfOutdated() {
			logger.Warnf("registrar indicates that ClinetConf is outdated")
		}

		err = reg.UnpackRegResp(dnsResp.GetBidirectionalResponse())
		if err != nil {
			logger.Warnf("failed to unpack registration response: %v", err)
			continue
		}
		return reg, nil
	}

	logger.WithField("maxTries", r.maxRetries).Warnf("all registration attemps failed")

	return nil, lib.ErrRegFailed
}

// Register prepares and sends the registration request.
func (r *AMPCacheRegistrar) Register(cjSession *tapdance.ConjureSession, ctx context.Context) (*tapdance.ConjureReg, error) {
	defer lib.SleepWithContext(ctx, r.connectionDelay)

	//	if r.bidirectional {
	return r.registerBidirectional(ctx, cjSession)
	//	}
	//	return r.registerUnidirectional(ctx, cjSession)
}

// PrepareRegKeys prepares key materials specific to the registrar
func (r *AMPCacheRegistrar) PrepareRegKeys(stationPubkey [32]byte, sessionSecret []byte) error {

	return nil
}
