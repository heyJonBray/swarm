// Copyright 2018 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package client

import (
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/hexutil"
	ethCrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/node"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/p2p/simulations"
	"github.com/ethereum/go-ethereum/p2p/simulations/adapters"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/ethersphere/swarm/network"
	"github.com/ethersphere/swarm/oldpss"
	"github.com/ethersphere/swarm/state"
	"github.com/ethersphere/swarm/testutil"
)

type protoCtrl struct {
	C        chan bool
	protocol *oldpss.Protocol
	run      func(*p2p.Peer, p2p.MsgReadWriter) error
}

var (
	// custom logging
	psslogmain   log.Logger
	pssprotocols map[string]*protoCtrl
	sendLimit    = uint16(256)
)

var services = newServices()

func init() {
	testutil.Init()
	rand.Seed(time.Now().Unix())

	adapters.RegisterServices(services)

	psslogmain = log.New("psslog", "*")

	pssprotocols = make(map[string]*protoCtrl)
}

// ping pong exchange across one expired symkey
func TestClientHandshake(t *testing.T) {
	sendLimit = 3

	clients, err := setupNetwork(2)
	if err != nil {
		t.Fatal(err)
	}

	lpsc, err := NewClientWithRPC(clients[0])
	if err != nil {
		t.Fatal(err)
	}
	rpsc, err := NewClientWithRPC(clients[1])
	if err != nil {
		t.Fatal(err)
	}
	lpssping := &oldpss.Ping{
		OutC: make(chan bool),
		InC:  make(chan bool),
		Pong: false,
	}
	rpssping := &oldpss.Ping{
		OutC: make(chan bool),
		InC:  make(chan bool),
		Pong: false,
	}
	lproto := oldpss.NewPingProtocol(lpssping)
	rproto := oldpss.NewPingProtocol(rpssping)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()
	err = lpsc.RunProtocol(ctx, lproto)
	if err != nil {
		t.Fatal(err)
	}
	err = rpsc.RunProtocol(ctx, rproto)
	if err != nil {
		t.Fatal(err)
	}
	topic := oldpss.PingTopic.String()

	var loaddr string
	err = clients[0].Call(&loaddr, "pss_baseAddr")
	if err != nil {
		t.Fatalf("rpc get node 1 baseaddr fail: %v", err)
	}
	var roaddr string
	err = clients[1].Call(&roaddr, "pss_baseAddr")
	if err != nil {
		t.Fatalf("rpc get node 2 baseaddr fail: %v", err)
	}

	var lpubkey string
	err = clients[0].Call(&lpubkey, "pss_getPublicKey")
	if err != nil {
		t.Fatalf("rpc get node 1 pubkey fail: %v", err)
	}
	var rpubkey string
	err = clients[1].Call(&rpubkey, "pss_getPublicKey")
	if err != nil {
		t.Fatalf("rpc get node 2 pubkey fail: %v", err)
	}

	err = clients[0].Call(nil, "pss_setPeerPublicKey", rpubkey, topic, roaddr)
	if err != nil {
		t.Fatal(err)
	}
	err = clients[1].Call(nil, "pss_setPeerPublicKey", lpubkey, topic, loaddr)
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(time.Second)

	roaddrbytes, err := hexutil.Decode(roaddr)
	if err != nil {
		t.Fatal(err)
	}
	err = lpsc.AddPssPeer(rpubkey, roaddrbytes, oldpss.PingProtocol)
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(time.Second)

	for i := uint16(0); i <= sendLimit; i++ {
		lpssping.OutC <- false
		got := <-rpssping.InC
		log.Warn("ok", "idx", i, "got", got)
		time.Sleep(time.Second)
	}

	rw := lpsc.peerPool[oldpss.PingTopic][rpubkey]
	lpsc.RemovePssPeer(rpubkey, oldpss.PingProtocol)
	if err := rw.WriteMsg(p2p.Msg{
		Size:    3,
		Payload: bytes.NewReader([]byte("foo")),
	}); err == nil {
		t.Fatalf("expected error on write")
	}
}

func setupNetwork(numnodes int) (clients []*rpc.Client, err error) {
	nodes := make([]*simulations.Node, numnodes)
	clients = make([]*rpc.Client, numnodes)
	if numnodes < 2 {
		return nil, fmt.Errorf("Minimum two nodes in network")
	}
	adapter := adapters.NewSimAdapter(services)
	net := simulations.NewNetwork(adapter, &simulations.NetworkConfig{
		ID:             "0",
		DefaultService: "bzz",
	})
	for i := 0; i < numnodes; i++ {
		nodeconf := adapters.RandomNodeConfig()
		nodeconf.Services = []string{"bzz", "oldpss"}
		nodes[i], err = net.NewNodeWithConfig(nodeconf)
		if err != nil {
			return nil, fmt.Errorf("error creating node 1: %v", err)
		}
		err = net.Start(nodes[i].ID())
		if err != nil {
			return nil, fmt.Errorf("error starting node 1: %v", err)
		}
		if i > 0 {
			err = net.Connect(nodes[i].ID(), nodes[i-1].ID())
			if err != nil {
				return nil, fmt.Errorf("error connecting nodes: %v", err)
			}
		}
		clients[i], err = nodes[i].Client()
		if err != nil {
			return nil, fmt.Errorf("create node 1 rpc client fail: %v", err)
		}
	}
	if numnodes > 2 {
		err = net.Connect(nodes[0].ID(), nodes[len(nodes)-1].ID())
		if err != nil {
			return nil, fmt.Errorf("error connecting first and last nodes")
		}
	}
	return clients, nil
}

func newServices() adapters.Services {
	stateStore := state.NewInmemoryStore()
	kademlias := make(map[enode.ID]*network.Kademlia)
	kademlia := func(id enode.ID) *network.Kademlia {
		if k, ok := kademlias[id]; ok {
			return k
		}
		params := network.NewKadParams()
		params.NeighbourhoodSize = 2
		params.MaxBinSize = 3
		params.MinBinSize = 1
		params.MaxRetries = 1000
		params.RetryExponent = 2
		params.RetryInterval = 1000000
		kademlias[id] = network.NewKademlia(id[:], params)
		return kademlias[id]
	}
	return adapters.Services{
		"oldpss": func(ctx *adapters.ServiceContext) (node.Service, error) {
			privkey, err := ethCrypto.GenerateKey()
			if err != nil {
				return nil, err
			}
			psparams := oldpss.NewParams().WithPrivateKey(privkey)
			pskad := kademlia(ctx.Config.ID)
			ps, err := oldpss.New(pskad, psparams)
			if err != nil {
				return nil, err
			}
			pshparams := oldpss.NewHandshakeParams()
			pshparams.SymKeySendLimit = sendLimit
			err = oldpss.SetHandshakeController(ps, pshparams)
			if err != nil {
				return nil, fmt.Errorf("handshake controller fail: %v", err)
			}
			return ps, nil
		},
		"bzz": func(ctx *adapters.ServiceContext) (node.Service, error) {
			addr := network.NewBzzAddrFromEnode(ctx.Config.Node())
			hp := network.NewHiveParams()
			hp.Discovery = false
			config := &network.BzzConfig{
				Address:    addr,
				HiveParams: hp,
			}
			return network.NewBzz(config, kademlia(ctx.Config.ID), stateStore, nil, nil, nil, nil), nil
		},
	}
}