package executorclient

import (
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// Dial connects with gRPC retries disabled, so no call is sent twice.
func Dial(
	target string,
	creds credentials.TransportCredentials,
	opts ...grpc.DialOption,
) (*grpc.ClientConn, error) {
	opts = append(opts, grpc.WithTransportCredentials(creds), grpc.WithDisableRetry())
	conn, err := grpc.NewClient(target, opts...)
	if err != nil {
		return nil, fmt.Errorf("executorclient: connect: %w", err)
	}
	return conn, nil
}
