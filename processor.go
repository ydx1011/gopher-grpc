package gophergrpc

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/xfali/xlog"
	"github.com/ydx1011/gopher-core/bean"
	"github.com/ydx1011/gopher-grpc/logger"
	"github.com/ydx1011/gopher-grpc/server"
	"github.com/ydx1011/yfig"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/grpclog"
	"net"
	"time"
)

const (
	defaultConnectTimeout = 120
)

type marshalFunc func(v interface{}) ([]byte, error)

type processor struct {
	logger xlog.Logger

	srvConf *serverConf
	srv     *grpc.Server
	cert    *tls.Certificate

	marshalFunc     marshalFunc
	recoveryHandler func(err error) error

	serversRegistry []server.RegistrarAware
	interceptors    []server.UnaryServerInterceptor
}

type serverConf struct {
	Host           string        `json:"host" yaml:"host"`
	Port           int           `json:"port" yaml:"port"`
	ConnectTimeout time.Duration `json:"connectTimeout" yaml:"connectTimeout"`

	Tls tlsConf `json:"tls" yaml:"tls"`
	Log logConf `json:"log" yaml:"log"`
}

type tlsConf struct {
	Cert string `json:"cert" yaml:"cert"`
	Key  string `json:"key" yaml:"key"`
}

type logConf struct {
	Disable bool `json:"disable" yaml:"disable"`
}

type Opt func(processor *processor)

func NewProcessor(opts ...Opt) *processor {
	ret := &processor{
		logger:      xlog.GetLogger(),
		marshalFunc: json.Marshal,
	}
	for _, opt := range opts {
		opt(ret)
	}
	return ret
}

func (p *processor) Init(conf yfig.Properties, container bean.Container) error {
	p.srvConf = &serverConf{}
	err := conf.GetValue("gopher.grpc.server", p.srvConf)
	if err != nil {
		return err
	}
	if p.srvConf.ConnectTimeout == 0 {
		p.srvConf.ConnectTimeout = defaultConnectTimeout
	}
	grpclog.SetLoggerV2(logger.New(p.logger))
	return nil
}

func (p *processor) Classify(o interface{}) (bool, error) {
	switch v := o.(type) {
	case server.RegistrarAware:
		p.serversRegistry = append(p.serversRegistry, v)
		return true, nil
	}
	return false, nil
}

func (p *processor) Process() error {
	err := p.processServer()
	if err != nil {
		p.logger.Errorln(err)
		return err
	}
	return p.processClient()
}

func (p *processor) processClient() error {
	return nil
}

func (p *processor) processServer() error {
	if p.srvConf != nil && p.srvConf.Port != 0 {
		lis, err := net.Listen("tcp", fmt.Sprintf("%s:%d",
			p.srvConf.Host, p.srvConf.Port))
		if err != nil {
			return err
		}

		var creds credentials.TransportCredentials
		if p.srvConf.Tls.Key != "" && p.srvConf.Tls.Cert != "" {
			creds, err = credentials.NewServerTLSFromFile(
				p.srvConf.Tls.Cert,
				p.srvConf.Tls.Key)
			if err != nil {
				return err
			}
		} else {
			if p.cert != nil {
				creds = credentials.NewServerTLSFromCert(p.cert)
			}
		}
		interceptors := make([]server.UnaryServerInterceptor, 0, 2+len(p.interceptors))
		interceptors = append(interceptors, p.recoveryFunc)
		if !p.srvConf.Log.Disable {
			interceptors = append(interceptors, p.loggingFunc)
		}
		interceptors = append(interceptors, p.interceptors...)
		if creds != nil {
			p.srv = grpc.NewServer(
				grpc.ConnectionTimeout(p.srvConf.ConnectTimeout*time.Second),
				grpc.Creds(creds),
				grpc.ChainUnaryInterceptor(interceptors...))
		} else {
			p.srv = grpc.NewServer(grpc.ConnectionTimeout(p.srvConf.ConnectTimeout*time.Second),
				grpc.ChainUnaryInterceptor(interceptors...))
		}

		for _, sr := range p.serversRegistry {
			sr.RegisterService(p.srv)
		}

		go func() {
			p.logger.Warnln(p.srv.Serve(lis))
		}()
	} else {
		p.logger.Warnln("gopher grpc run without server. ")
	}
	return nil
}

func (p *processor) BeanDestroy() error {
	if p.srv != nil {
		p.srv.Stop()
	}
	return nil
}

func (p *processor) recoveryFunc(
	ctx context.Context,
	req interface{},
	info *grpc.UnaryServerInfo,
	handler grpc.UnaryHandler) (resp interface{}, err error) {
	defer func() {
		if o := recover(); o != nil {
			if v, ok := o.(error); ok {
				err = v
			} else {
				err = fmt.Errorf("%v", o)
			}
			if p.recoveryHandler != nil {
				err = p.recoveryHandler(err)
			} else {
				p.logger.Errorln(err)
			}
		}
	}()
	return handler(ctx, req)
}

func (p *processor) loggingFunc(
	ctx context.Context,
	req interface{},
	info *grpc.UnaryServerInfo,
	handler grpc.UnaryHandler) (resp interface{}, err error) {
	reqV := defaultMarshal(req, p.marshalFunc)
	id := RandomId(32)
	p.logger.Infof("Grpc server Request[%s]: %s request: %s\n",
		id, info.FullMethod, reqV)
	resp, err = handler(ctx, req)
	if err != nil {
		p.logger.Infof("Grpc server Response[%s]: %s err: %s response: %s\n")
	} else {
		respV := defaultMarshal(resp, p.marshalFunc)
		p.logger.Infof("Grpc server Response[%s]: %s response: %s\n",
			id, info.FullMethod, respV)
	}
	return resp, err
}

func defaultMarshal(v interface{}, f marshalFunc) string {
	d, err := f(v)
	if err == nil {
		return string(d)
	}
	return fmt.Sprintf("%v", v)
}

type svrOpts struct {
}

var ServerOpts svrOpts

func (o svrOpts) RecoveryHandler(h func(error) error) Opt {
	return func(processor *processor) {
		processor.recoveryHandler = h
	}
}

func (o svrOpts) AddUnaryServerInterceptors(interceptors ...server.UnaryServerInterceptor) Opt {
	return func(processor *processor) {
		processor.interceptors = interceptors
	}
}

func RandomId(length int) string {
	b := make([]byte, length)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return base64.URLEncoding.EncodeToString(b)
}
