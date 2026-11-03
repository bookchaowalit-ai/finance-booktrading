package grpcserver

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const testServiceToken = "grpc-service-token"

func withAuth(value string) context.Context {
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", value))
}

func TestUnaryRequiresServiceToken(t *testing.T) {
	s := &GRPCServer{}
	s.SetServiceToken(testServiceToken)
	info := &grpc.UnaryServerInfo{FullMethod: "/trading.OrderExecutionService/ExecuteOrder"}

	cases := map[string]context.Context{
		"no metadata": context.Background(),
		"wrong token": withAuth("Bearer nope"),
		"no scheme":   withAuth(testServiceToken),
	}
	for name, ctx := range cases {
		called := false
		_, err := s.unaryInterceptor(ctx, nil, info, func(context.Context, interface{}) (interface{}, error) {
			called = true
			return nil, nil
		})
		if status.Code(err) != codes.Unauthenticated || called {
			t.Errorf("%s: got %v, handler called=%t; want Unauthenticated and no call", name, err, called)
		}
	}

	called := false
	if _, err := s.unaryInterceptor(withAuth("Bearer "+testServiceToken), nil, info,
		func(context.Context, interface{}) (interface{}, error) { called = true; return nil, nil }); err != nil || !called {
		t.Fatalf("valid token: err=%v called=%t", err, called)
	}
}

type fakeStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (f fakeStream) Context() context.Context { return f.ctx }

func TestStreamRequiresServiceToken(t *testing.T) {
	s := &GRPCServer{}
	s.SetServiceToken(testServiceToken)
	info := &grpc.StreamServerInfo{FullMethod: "/trading.MarketDataService/SubscribeMarketData"}

	called := false
	err := s.streamInterceptor(nil, fakeStream{ctx: context.Background()}, info,
		func(interface{}, grpc.ServerStream) error { called = true; return nil })
	if status.Code(err) != codes.Unauthenticated || called {
		t.Fatalf("no token: got %v called=%t", err, called)
	}

	err = s.streamInterceptor(nil, fakeStream{ctx: withAuth("Bearer " + testServiceToken)}, info,
		func(interface{}, grpc.ServerStream) error { called = true; return nil })
	if err != nil || !called {
		t.Fatalf("valid token: err=%v called=%t", err, called)
	}
}

func TestStartRefusesWithoutServiceToken(t *testing.T) {
	s := &GRPCServer{port: "0"}
	if err := s.Start(); err == nil {
		s.Stop()
		t.Fatal("gRPC must not serve without a service token")
	}
}
