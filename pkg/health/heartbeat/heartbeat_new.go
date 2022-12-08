package heartbeat

import (
	"context"
	"errors"
	"fmt"
	"time"

	etcdErr "github.com/gardener/etcd-backup-restore/pkg/errors"
	"github.com/gardener/etcd-backup-restore/pkg/monitor"
	"github.com/sirupsen/logrus"
	v1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	errEtcdStateNotifierChClosed = errors.New("etcdState notifier channel is closed")
)

// LeaseUpdater updates the lease for this etcd member. A lease represents a heartbeat which needs to be renewed periodically which indicates that the member is live.
type LeaseUpdater interface {
	// Run starts a loop which periodically renews/updates the lease for this etcd member.
	Run(context.Context) error
}

// NewLeaseUpdater creates a new LeaseUpdater.
func NewLeaseUpdater(etcdStateNotifier monitor.EtcdStateNotifier, clientSet client.Client, metadata map[string]string, podName string, podNamespace string, k8sClientOpsTimeout time.Duration, logger *logrus.Entry) LeaseUpdater {
	notifierCh := etcdStateNotifier.Subscribe("heartbeat-leaseUpdater")

	return &leaseUpdater{
		etcdStateNotifier:   etcdStateNotifier,
		k8sClient:           clientSet,
		podName:             podName,
		podNamespace:        podNamespace,
		notifierCh:          notifierCh,
		metadata:            metadata,
		k8sClientOpsTimeout: k8sClientOpsTimeout,
		logger:              logger,
	}
}

// leaseUpdater implements LeaseUpdater.
type leaseUpdater struct {
	logger              *logrus.Entry
	renewDuration       time.Duration
	k8sClient           client.Client
	k8sClientOpsTimeout time.Duration
	podName             string
	podNamespace        string
	etcdStateNotifier   monitor.EtcdStateNotifier
	notifierCh          <-chan monitor.EtcdMemberState
	etcdMemberState     *monitor.EtcdMemberState
	metadata            map[string]string // metadata is currently added as annotations to the k8s lease object
}

func (lu *leaseUpdater) Run(ctx context.Context) error {
	go func() {
		if err := lu.etcdMemberStateWatcher(ctx); err != nil {
			lu.logger.Errorf("etcdMemberState watcher has been closed: %v", err)
			// TODO: Check if we need handle the closing of notifier channel.
			// If yes, then we need to create drain and close functionality in etcd monitor.
		}
	}()

	for {
		select {
		case <-ctx.Done():
			lu.logger.Error("context has been cancelled, exiting leaseUpdater: %v", ctx.Err())
			return ctx.Err()
		case <-time.After(lu.renewDuration):
			if lu.canRenewLease() {
				if err := lu.renewMemberLease(ctx); err != nil {
					lu.logger.Error(err)
				}
			}
		}
	}
}

func (lu *leaseUpdater) etcdMemberStateWatcher(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case etcdMemberState, ok := <-lu.notifierCh:
			if !ok {
				return errEtcdStateNotifierChClosed
			}
			lu.etcdMemberState = &etcdMemberState
		}
	}
}

func (lu *leaseUpdater) renewMemberLease(parentCtx context.Context) error {
	existingMemberLease, err := lu.getMemberLease(parentCtx)
	if err != nil {
		return err
	}
	newMemberLease := lu.createNewMemberLease(existingMemberLease)
	return lu.patchMemberLease(parentCtx, existingMemberLease, newMemberLease)
}

func (lu *leaseUpdater) canRenewLease() bool {
	return lu.etcdMemberState != nil && *lu.etcdMemberState != monitor.Unknown
}

func (lu *leaseUpdater) getMemberLease(parentCtx context.Context) (*v1.Lease, error) {
	ctx, cancel := context.WithTimeout(parentCtx, lu.k8sClientOpsTimeout)
	defer cancel()

	// Fetch lease associated with member
	memberLease := &v1.Lease{}
	err := lu.k8sClient.Get(ctx, client.ObjectKey{
		Namespace: lu.podNamespace,
		Name:      lu.podName,
	}, memberLease)
	if err != nil {
		return nil, &etcdErr.EtcdError{
			Err:     err,
			Message: fmt.Sprintf("Could not fetch member lease: %v", err),
		}
	}
	return memberLease, nil
}

func (lu *leaseUpdater) createNewMemberLease(existingMemberLease *v1.Lease) *v1.Lease {
	// Change HolderIdentity and RenewTime of lease
	newMemberLease := existingMemberLease.DeepCopy()
	newMemberLease.Spec.HolderIdentity = pointer.StringPtr(fmt.Sprint(lu.etcdMemberState))

	// Update only keys from metadata
	if newMemberLease.Annotations == nil {
		newMemberLease.Annotations = map[string]string{}
	}
	for k, v := range lu.metadata {
		newMemberLease.Annotations[k] = v
	}

	// Renew the lease time
	renewedTime := time.Now()
	newMemberLease.Spec.RenewTime = &metav1.MicroTime{Time: renewedTime}

	return newMemberLease
}

func (lu *leaseUpdater) patchMemberLease(parentCtx context.Context, existingMemberLease *v1.Lease, renewedMemberLease *v1.Lease) error {
	ctx, cancel := context.WithTimeout(parentCtx, lu.k8sClientOpsTimeout)
	defer cancel()

	err := lu.k8sClient.Patch(ctx, renewedMemberLease, client.MergeFrom(existingMemberLease))
	if err != nil {
		return &etcdErr.EtcdError{
			Err:     err,
			Message: fmt.Sprintf("Failed to renew member lease: %v", err),
		}
	}
	return nil
}
