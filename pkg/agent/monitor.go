package agent

import "context"

type EtcdMemberState int

const ()

type MemberStatus struct {
}

type NotificationType int

const (
	LeaderChangeNotification NotificationType = iota
	MemberStateChangeNotification
	AlarmNotification
)

type EtcdMonitor interface {
	Run(ctx context.Context) error
	Subscribe(string, NotificationType) <-chan Notification
	Close()
}

func NewEtcdMonitor() EtcdMonitor {

}

type Notification struct {
	MemberState EtcdMemberState
	Message     string
	Error       error
}

type subscribedChannel struct {
	subscriber string
	ch         <-chan Notification
}

type etcdMonitor struct {
	subscriptions map[NotificationType][]subscribedChannel
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
			return ctx.Err()

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
