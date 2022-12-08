package agent

import (
	"context"
	"fmt"
	"time"

	. "github.com/gardener/etcd-backup-restore/pkg/errors"
	"github.com/gardener/etcd-backup-restore/pkg/etcdutil/client"
	brtypes "github.com/gardener/etcd-backup-restore/pkg/types"
	"github.com/sirupsen/logrus"
)

type EtcdMemberState int

const (
	Leader EtcdMemberState = iota
	Follower
	Learner
	Unknown
)

//go:generate stringer -type=EtcdMemberState

const (
	// NoLeaderState defines the state when etcd returns LeaderID as 0.
	NoLeaderState uint64 = 0
)

type EtcdMaintenanceClientCreatorFn func(*brtypes.EtcdConnectionConfig) (client.MaintenanceCloser, error)

type EtcdMonitor interface {
	Run(ctx context.Context) error
	EtcdStateNotifier
	Close()
}

type EtcdStateNotifier interface {
	Subscribe(name string) <-chan EtcdMemberState
	Unsubscribe(name string)
}

func NewEtcdMonitor(etcdConnectionConfig *brtypes.EtcdConnectionConfig, clientCreatorFn EtcdMaintenanceClientCreatorFn, etcdConnectionTimeout time.Duration, pollInterval time.Duration) (EtcdMonitor, error) {
	client, err := clientCreatorFn(etcdConnectionConfig)
	if err != nil {
		return nil, err
	}

	return &etcdMonitor{
		etcdClient:            client,
		pollInterval:          pollInterval,
		etcdConnectionTimeout: etcdConnectionTimeout,
		endpoints:             etcdConnectionConfig.Endpoints,
	}, nil
}

type subscription struct {
	subscriber string
	ch         chan EtcdMemberState
}

type etcdMonitor struct {
	logger                *logrus.Entry
	pollInterval          time.Duration
	etcdConnectionTimeout time.Duration
	etcdClient            client.MaintenanceCloser
	endpoints             []string
	subscriptions         []subscription
}

func (em *etcdMonitor) Unsubscribe(name string) {
	// TODO Ishan to implement
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
			etcdMemberState, _ := em.pollEtcd(ctx)
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

func (em *etcdMonitor) pollEtcd(ctx context.Context) (EtcdMemberState, error) {
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
