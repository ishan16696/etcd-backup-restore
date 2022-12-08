package heartbeat

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gardener/etcd-backup-restore/pkg/agent"
	etcdErr "github.com/gardener/etcd-backup-restore/pkg/errors"
	"github.com/sirupsen/logrus"
	v1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	errEtcdStateNotifierChClosed = errors.New("etcdState notifier channel is closed")
)

type HeartbeatHandler interface {
	Run(context.Context) error
}

func NewHeartbeatHandler(etcdStateNotifier agent.EtcdStateNotifier, clientSet client.Client, metadata map[string]string, podName string, namespace string, k8sClientOpsTimeout time.Duration, logger *logrus.Entry) HeartbeatHandler {
	notifierCh := etcdStateNotifier.Subscribe("heartbeat-handler")

	return &heartbeatHandler{
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

// heartbeatHandler contains information to perform regular heart beats in a Kubernetes cluster.
type heartbeatHandler struct {
	logger              *logrus.Entry
	renewDuration       time.Duration
	k8sClient           client.Client
	k8sClientOpsTimeout time.Duration
	podName             string
	podNamespace        string
	etcdStateNotifier   agent.EtcdStateNotifier
	notifierCh          <-chan agent.EtcdMemberState
	etcdMemberstate     *agent.EtcdMemberState
	metadata            map[string]string // metadata is currently added as annotations to the k8s lease object
}

func (h *heartbeatHandler) Run(ctx context.Context) error {
	go func() {
		if err := h.etcdMemberStateWatcher(ctx); err != nil {
			h.logger.Errorf("etcdMemberState watcher has been closed: %v", err)
			// TODO: Check if we need handle the closing of notifier channel.
			// If yes, then we need to create drain and close functionality in etcd monitor.
		}
	}()

	for {
		select {
		case <-ctx.Done():
			h.logger.Error("exiting heartbeatHandler: %v", ctx.Err())
			return ctx.Err()
		case <-time.After(h.renewDuration):
			if err := h.renewMemberLease(ctx); err != nil {
				h.logger.Error(err)
			}
		}
	}
}

func (h *heartbeatHandler) etcdMemberStateWatcher(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case etcdMemberState, ok := <-h.notifierCh:
			if !ok {
				return errEtcdStateNotifierChClosed
			}
			h.etcdMemberstate = &etcdMemberState
		}
	}
}

func (h *heartbeatHandler) renewMemberLease(pCtx context.Context) error {

	ctx, cancel := context.WithTimeout(pCtx, h.k8sClientOpsTimeout)
	defer cancel()

	// Fetch lease associated with member
	memberLease := &v1.Lease{}
	err := h.k8sClient.Get(ctx, client.ObjectKey{
		Namespace: h.podNamespace,
		Name:      h.podName,
	}, memberLease)
	if err != nil {
		return &etcdErr.EtcdError{
			Err:     err,
			Message: fmt.Sprintf("Could not fetch member lease: %v", err),
		}
	}

	// Change HolderIdentity and RenewTime of lease
	renewedMemberLease := memberLease.DeepCopy()
	renewedMemberLease.Spec.HolderIdentity = pointer.StringPtr(fmt.Sprint(h.etcdMemberstate))

	// Update only keys from metadata
	if renewedMemberLease.Annotations == nil {
		renewedMemberLease.Annotations = map[string]string{}
	}
	for k, v := range h.metadata {
		renewedMemberLease.Annotations[k] = v
	}

	// Renew the lease time
	renewedTime := time.Now()
	renewedMemberLease.Spec.RenewTime = &metav1.MicroTime{Time: renewedTime}

	err = h.k8sClient.Patch(ctx, renewedMemberLease, client.MergeFrom(memberLease))
	if err != nil {
		return &etcdErr.EtcdError{
			Err:     err,
			Message: fmt.Sprintf("Failed to renew member lease: %v", err),
		}
	}
	return nil
}
