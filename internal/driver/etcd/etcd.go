// Package etcd là driver "etcd". FR-07: snapshot Raft, restore ra data dir mới.
package etcd

import "cistern/internal/driver"

func init() {
	driver.Register("etcd", func(p driver.Params) (driver.Driver, error) {
		// TODO: hiện thực driver thật.
		return driver.NewStub("etcd", p), nil
	})
}
