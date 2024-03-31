package server

import (
	"google.golang.org/grpc"
)

type RegistrarAware interface {
	RegisterService(registrar grpc.ServiceRegistrar)
}

type UnaryServerInterceptor = grpc.UnaryServerInterceptor
