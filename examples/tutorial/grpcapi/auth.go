package grpcapi

import (
	"context"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"example.com/servoorders/auth"
	"example.com/servoorders/session"
)

// claimsKey is unexported and of an unexported type, so nothing outside
// this package can set or read it — the same reason api/middleware.go
// uses a private key type.
type contextKey int

const claimsKey contextKey = 0

// authInterceptor is the gRPC equivalent of requireAuth. The shape
// difference from both HTTP variants is worth seeing: there is no route
// group and no per-handler wrapper, because a unary interceptor sees
// every call and decides per method name.
//
// The token lives in metadata rather than a header — the same bearer
// string, carried by a different transport convention.
func authInterceptor(issuer *auth.Issuer) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		// Login is the one method that cannot require a token, since it
		// is how a caller gets one. Listing exceptions by full method
		// name means adding an unauthenticated method is a deliberate
		// edit here, not something a new registration does silently.
		if info.FullMethod == "/ordersv1.Orders/Login" {
			return handler(ctx, req)
		}

		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "missing metadata")
		}
		values := md.Get("authorization")
		if len(values) == 0 {
			return nil, status.Error(codes.Unauthenticated, "missing authorization metadata")
		}
		token, ok := strings.CutPrefix(values[0], "Bearer ")
		if !ok || token == "" {
			return nil, status.Error(codes.Unauthenticated, "malformed authorization metadata")
		}

		claims, err := issuer.Verify(token)
		if err != nil {
			return nil, status.Error(codes.Unauthenticated, "invalid or expired token")
		}

		// Both go in for the same reason they do in the HTTP variants:
		// the claims are what handlers read, and the session key is what
		// servo's generated accessor reads — see
		// docs/tutorial/14-scoped-instances.md.
		ctx = context.WithValue(ctx, claimsKey, claims)
		ctx = session.WithUser(ctx, session.UserID(claims.UserID.String()))
		return handler(ctx, req)
	}
}

func claimsFrom(ctx context.Context) auth.Claims {
	// The interceptor guarantees this for every method except Login,
	// which never calls it.
	claims, _ := ctx.Value(claimsKey).(auth.Claims)
	return claims
}
