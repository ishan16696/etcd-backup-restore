package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/gardener/etcd-backup-restore/pkg/etcdutil/client"
	brtypes "github.com/gardener/etcd-backup-restore/pkg/types"
	"github.com/sirupsen/logrus"
)

type EtcdMemberState int

const (
	Leader EtcdMemberState = iota
	Follower
	Learner
)

const (
	// NoLeaderState defines the state when etcd returns LeaderID as 0.
	NoLeaderState uint64 = 0
)

type memberInfo struct {
	etcdMemberState EtcdMemberState
	message         string
}

type NotificationType int

const (
	LeaderChangeNotification NotificationType = iota
	MemberStateChangeNotification
	AlarmNotification
)

type EtcdMaintenanceClientCreatorFn func(*brtypes.EtcdConnectionConfig) (client.MaintenanceCloser, error)

type EtcdMonitor interface {
	Run(ctx context.Context) error
	Subscribe(string, NotificationType) <-chan Notification
	Close()
}

func NewEtcdMonitor(etcdConnectionConfig *brtypes.EtcdConnectionConfig, clientCreatorFn EtcdMaintenanceClientCreatorFn, etcdConnectionTimeout time.Duration, pollInterval time.Duration, endpoints []string) (EtcdMonitor, error) {
	client, err := clientCreatorFn(etcdConnectionConfig)
	if err != nil {
		return nil, err
	}

	return &etcdMonitor{
		etcdClient:            client,
		pollInterval:          pollInterval,
		etcdConnectionTimeout: etcdConnectionTimeout,
		endpoints:             endpoints,
		subscriptions:         make(map[NotificationType][]subscribedChannel),
	}, nil
}

type Notification struct {
	MemberState EtcdMemberState
	Message     string
	Error       error
}

type subscribedChannel struct {
	subscriber string
	ch         chan Notification
}

type etcdMonitor struct {
	logger                *logrus.Entry
	pollInterval          time.Duration
	etcdConnectionTimeout time.Duration
	etcdClient            client.MaintenanceCloser
	endpoints             []string
	subscriptions         map[NotificationType][]subscribedChannel
}

func (em *etcdMonitor) Subscribe(name string, n NotificationType) <-chan Notification {
	if ch := em.getSubscribedChannel(name, n); ch != nil {
		return ch
	}
	ch := make(chan Notification, 1)
	subscribedChannels := em.subscriptions[n]
	subscribedChannels = append(subscribedChannels, subscribedChannel{subscriber: name, ch: ch})
	return ch
}

func (em *etcdMonitor) Run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			em.Close()
			return ctx.Err()

		case <-time.After(em.pollInterval):
			em.pollEtcd(ctx)
		}
	}
}

func (em *etcdMonitor) Close() {
	defer func() {
		_ = em.etcdClient.Close()
	}()

	for _, subscribedChannels := range em.subscriptions {
		for _, subscribedChannel := range subscribedChannels {
			close(subscribedChannel.ch)
		}
	}
}

func (em *etcdMonitor) getSubscribedChannel(name string, n NotificationType) <-chan Notification {
	subscribedChannels, ok := em.subscriptions[n]
	if !ok {
		return nil
	}
	for _, subscribedCh := range subscribedChannels {
		if subscribedCh.subscriber == name {
			return subscribedCh.ch
		}
	}
	return nil
}

func (em *etcdMonitor) pollEtcd(ctx context.Context) (memberInfo, error) {
	var endPoint string

	if len(em.endpoints) > 0 {
		endPoint = em.endpoints[0]
	} else {
		return memberInfo{
			message: fmt.Sprintf("some msg.."),
		}, fmt.Errorf("etcd endpoints are not passed correctly")
	}

	ctx, cancelFn := context.WithTimeout(ctx, em.etcdConnectionTimeout)
	defer cancelFn()

	response, err := em.etcdClient.Status(ctx, endPoint)
	if err != nil {
		return memberInfo{
			message: fmt.Sprintf("some msg.."),
		}, err
	}

	if response.Header.GetMemberId() == response.Leader {
		return memberInfo{
			etcdMemberState: Leader,
		}, nil
	} else if response.Leader == NoLeaderState {
		return memberInfo{
			message: fmt.Sprintf("currently there is no etcd leader present may be due to etcd quorum loss or election is being held"),
		}, nil
	} else if response.IsLearner {
		return memberInfo{
			etcdMemberState: Learner,
		}, nil
	}

	return memberInfo{
		etcdMemberState: Follower,
	}, nil
}
