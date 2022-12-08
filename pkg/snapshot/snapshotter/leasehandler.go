package snapshotter

import (
	"context"

	brtypes "github.com/gardener/etcd-backup-restore/pkg/types"
	"github.com/sirupsen/logrus"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	snaphotLeaseKey = "etcd.gardener.cloud/snaphot"
)

type SnapshotLeaseHandler interface {
	UpdateFullSnapshotLease(ctx context.Context, k8sClientset client.Client, logger *logrus.Entry, fullSnapshot *brtypes.Snapshot) error
	UpdateDeltaSnapshotLease(ctx context.Context, k8sClientset client.Client, store brtypes.SnapStore, logger *logrus.Entry) error
}
