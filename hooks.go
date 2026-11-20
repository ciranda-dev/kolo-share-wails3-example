package main

import (
	"context"

	koloblobs "github.com/ciranda-dev/kolo-blobs"
	kolodht "github.com/ciranda-dev/kolo-dht"
	koloid "github.com/ciranda-dev/kolo-id"
	koloswarm "github.com/ciranda-dev/kolo-swarm"
)

// testHooks let the tests play a hostile peer or hold a step; the app leaves every one nil.
type testHooks struct {
	identity    *koloid.Identity                          // an identity kept in memory instead of the identity file
	wrapContent func(koloblobs.Backend) koloblobs.Backend // wraps what a receive reads, to play a peer that sends bad chunks
	tamperOffer func(*offer)                              // changes what an offer says, to play a peer that lies
	netGate     chan struct{}                             // the network stays "starting" until it is closed
	notchHook   func()                                    // runs between reading the notch state and sending it
	dht         *kolodht.Config                           // the kolo-dht settings of every swarm, to play a firewall
	noFeatures  bool                                      // plays an app that does not know the features stream: it sends none, and PREPARE is a protocol error
	swarmCfg    func(*koloswarm.Config)                   // sees the config of the session swarm before it opens
	pairCfg     func(*koloswarm.Config)                   // sees the config of a pairing swarm before it opens
	prepStep    func(ctx context.Context, n int)          // runs at every chunk a send stores, to hold or slow a preparation
	freeSpace   func(dir string) (int64, error)           // the free bytes of the volume of dir, to play a full disk or a large one
	volume      func(dir string) string                   // the name of the volume of dir, to play two disks
	rules       *lengthRules                              // the names and paths this computer can save, to play another system
}
