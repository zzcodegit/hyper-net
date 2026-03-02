//go:build integration

package smartclient

import (
	"context"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"hypernet-node/pkg/config"
	hypernode "hypernet-node/hypernet/core/network/node"
	"hypernet-node/hypernet/core/routing"
	"hypernet-node/pkg/protoadapters/plain"
	"hypernet-node/pkg/protoadapters/trojan"
	"hypernet-node/pkg/protoadapters/vless"
	"hypernet-node/hypernet/services/proxy/protomanager"
	"hypernet-node/hypernet/services/proxy/protonegotiate"
	ping "hypernet-node/hypernet/core/network/ping"
	proxy "hypernet-node/hypernet/services/proxy/server"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// (tests content copied 1:1 from pkg/smartclient/smartclient_integration_test.go)

