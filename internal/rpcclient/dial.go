// Package rpcclient dials another node's NodeService.
//
// Uses insecure gRPC transport for v0 — transport encryption is an open
// question, not yet decided.
package rpcclient

import (
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"sporemachine/internal/sporepb"
)

func Dial(addr string) (sporepb.NodeServiceClient, *grpc.ClientConn, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, nil, err
	}
	return sporepb.NewNodeServiceClient(conn), conn, nil
}
