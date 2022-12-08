package monitor

import (
	"context"
	"fmt"
	"time"

	. "github.com/gardener/etcd-backup-restore/pkg/errors"
	"github.com/gardener/etcd-backup-restore/pkg/etcdutil/client"
	brtypes "github.com/gardener/etcd-backup-restore/pkg/types"
	"github.com/sirupsen/logrus"
)

// EtcdMemberState represents different etcd member states.
type EtcdMemberState int

const (
	// Leader indicates that this member is a leader.
	Leader EtcdMemberState = iota
	// Follower indicate that this member is a follower.
	Follower
	// Learner indicates that this member is a leaner.
	Learner
	// Unknown indicates that backup-restore is unable to determine the state of the etcd peer container.
	Unknown
)

//go:generate stringer -type=EtcdMemberState

const (
	// NoLeaderState indicates that there is no leader. This could happen if a leader election is underway or there is a quorum loss.
	NoLeaderState uint64 = 0
)

// EtcdMaintenanceClientCreatorFn is a function alias for a function which knows how to create a client.MaintenanceCloser given a brtypes.EtcdConnectionConfig.
type EtcdMaintenanceClientCreatorFn func(*brtypes.EtcdConnectionConfig) (client.MaintenanceCloser, error)

// EtcdMonitor is a facade to monitor etcd peer container and to send notifications to all subscribers on state of etcd.
// This monitor will periodically poll the etcd container for its state and the received state is then sent to multicasted to all subscribers.
type EtcdMonitor interface {
	// Run starts the monitor which will start to poll the etcd container for its status.
	Run(ctx context.Context) error
	EtcdStateNotifier
	// Close closes the monitor and also closes all subscribers.
	Close()
}

// EtcdStateNotifier is facade which allows creating and removing subscriptions. Each subscriber starts to receive notifications on etcd state.
type EtcdStateNotifier interface {
	// Subscribe registers a consumer with a given name to receive etcd state notifications on a dedicated channel which is maintained by the monitor.
	Subscribe(name string) <-chan EtcdMemberState
	// Unsubscribe unregisters the consumer and closes its subscription. This consumer will no longer receive etcd state notifications.
	Unsubscribe(name string)
}

// NewEtcdMonitor creates a new instance of EtcdMonitor.
func NewEtcdMonitor(etcdConnectionConfig *brtypes.EtcdConnectionConfig, clientCreatorFn EtcdMaintenanceClientCreatorFn, etcdConnectionTimeout time.Duration, pollInterval time.Duration) (EtcdMonitor, error) {
	etcdClient, err := clientCreatorFn(etcdConnectionConfig)
	if err != nil {
		return nil, err
	}

	return &etcdMonitor{
		etcdClient:            etcdClient,
		pollInterval:          pollInterval,
		etcdConnectionTimeout: etcdConnectionTimeout,
		endpoints:             etcdConnectionConfig.Endpoints,
	}, nil
}

// subscription represents a subscription for a consumer who has subscribed for etcd state notifications.
type subscription struct {
	// subscriber is the name of the subscriber. All subscribers should have unique name.
	subscriber string
	// ch is the channel on which the subscriber will receive etcd state notifications.
	ch chan EtcdMemberState
}

// etcdMonitor implements EtcdMonitor interface.
type etcdMonitor struct {
	logger                *logrus.Entry
	pollInterval          time.Duration
	etcdConnectionTimeout time.Duration
	etcdClient            client.MaintenanceCloser
	endpoints             []string
	subscriptions         []subscription
}

func (em *etcdMonitor) Unsubscribe(name string) {
	index := -1
	for i, s := range em.subscriptions {
		if s.subscriber == name {
			index = i
			break
		}
	}
	if index >= 0 {
		// found the subscription, close the channel and remove the entry from the slice of subscriptions.
		close(em.subscriptions[index].ch)
		em.subscriptions[index] = em.subscriptions[0]
		em.subscriptions = em.subscriptions[1:]
	}
}

func (em *etcdMonitor) Subscribe(name string) <-chan EtcdMemberState {
	if ch := em.getSubscribedChannel(name); ch != nil {
		return ch
	}
	ch := make(chan EtcdMemberState, 1)
	em.subscriptions = append(em.subscriptions, subscription{subscriber: name, ch: ch})
	return ch
}

func (em *etcdMonitor) Run(ctx context.Context) error {
	defer em.Close()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(em.pollInterval):
			etcdMemberState, _ := em.getEtcdState(ctx)
			em.sendNotifications(etcdMemberState)
		}
	}
}

func (em *etcdMonitor) Close() {
	defer func() {
		_ = em.etcdClient.Close()
	}()

	for _, subscribedChannel := range em.subscriptions {
		close(subscribedChannel.ch)
	}
}

func (em *etcdMonitor) getSubscribedChannel(name string) <-chan EtcdMemberState {
	for _, subscribedCh := range em.subscriptions {
		if subscribedCh.subscriber == name {
			return subscribedCh.ch
		}
	}
	return nil
}

func (em *etcdMonitor) getEtcdState(ctx context.Context) (EtcdMemberState, error) {
	ctx, cancelFn := context.WithTimeout(ctx, em.etcdConnectionTimeout)
	defer cancelFn()

	endPoint := em.endpoints[0]
	response, err := em.etcdClient.Status(ctx, endPoint)
	if err != nil {
		msg := "error fetching etcd status"
		if response != nil {
			msg = fmt.Sprintf("%s %v", msg, response.Errors)
		}
		return Unknown, EtcdError{
			Code:    ErrorFetchingEtcdStatus,
			Message: msg,
			Err:     err,
		}
	}

	if response.Header.GetMemberId() == response.Leader {
		return Leader, nil
	} else if response.Leader == NoLeaderState {
		return Unknown, EtcdError{
			Code:    NoEtcLeader,
			Message: "Currently there is no etcd leader present. It may be due to etcd quorum loss or election being held",
			Err:     nil,
		}
	} else if response.IsLearner {
		return Learner, nil
	}

	return Follower, nil
}

func (em *etcdMonitor) sendNotifications(state EtcdMemberState) {
	for _, s := range em.subscriptions {
		s.ch <- state
	}
}
