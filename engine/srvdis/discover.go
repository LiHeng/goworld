package srvdis

import (
	"context"

	"encoding/json"

	"strings"

	"github.com/coreos/etcd/clientv3"
	"github.com/coreos/etcd/clientv3/namespace"
	"github.com/coreos/etcd/mvcc/mvccpb"
	"github.com/xiaonanln/goworld/engine/gwlog"
)

type serviceTypeMgr struct {
	services map[string]ServiceRegisterInfo
}

func newServiceTypeMgr() *serviceTypeMgr {
	return &serviceTypeMgr{
		services: map[string]ServiceRegisterInfo{},
	}
}

func (mgr *serviceTypeMgr) registerService(srvid string, info ServiceRegisterInfo) {
	mgr.services[srvid] = info
}

func (mgr *serviceTypeMgr) unregisterService(srvid string) {
	delete(mgr.services, srvid)
}

var (
	aliveServicesByType = map[string]*serviceTypeMgr{}
)

func VisitServicesByType(srvtype string, cb func(srvid string, info ServiceRegisterInfo)) {
	mgr := aliveServicesByType[srvtype]
	if mgr == nil {
		return
	}

	for srvid, info := range mgr.services {
		cb(srvid, info)
	}
}

func VisitServicesByTypePrefix(srvtypePrefix string, cb func(srvtype, srvid string, info ServiceRegisterInfo)) {
	for srvtype, mgr := range aliveServicesByType {
		if !strings.HasPrefix(srvtype, srvtypePrefix) {
			continue
		}

		for srvid, info := range mgr.services {
			cb(srvtype, srvid, info)
		}
	}
}

func watchRoutine(ctx context.Context, cli *clientv3.Client, delegate ServiceDelegate) {
	kv := clientv3.NewKV(cli)
	if srvdisNamespace != "" {
		kv = namespace.NewKV(kv, srvdisNamespace)
	}

	rangeResp, err := kv.Get(ctx, "/srvdis/", clientv3.WithPrefix())
	if err != nil {
		gwlog.Fatal(err)
	}

	for _, kv := range rangeResp.Kvs {
		handlePutServiceRegisterData(delegate, kv.Key, kv.Value)
	}

	w := clientv3.NewWatcher(cli)
	if srvdisNamespace != "" {
		w = namespace.NewWatcher(w, srvdisNamespace)
	}

	ch := w.Watch(ctx, "/srvdis/", clientv3.WithPrefix(), clientv3.WithRev(rangeResp.Header.Revision+1))
	for resp := range ch {
		for _, event := range resp.Events {
			if event.Type == mvccpb.PUT {
				//gwlog.Infof("watch resp: %v, created=%v, cancelled=%v, events=%q", resp, resp.Created, resp.Canceled, resp.Events[0].Kv.Key)
				handlePutServiceRegisterData(delegate, event.Kv.Key, event.Kv.Value)
			} else if event.Type == mvccpb.DELETE {
				handleDeleteServiceRegisterData(delegate, event.Kv.Key)
			}
		}
	}
}

func handlePutServiceRegisterData(delegate ServiceDelegate, key []byte, val []byte) {
	srvtype, srvid := parseRegisterPath(key)
	var registerInfo ServiceRegisterInfo
	err := json.Unmarshal(val, &registerInfo)
	if err != nil {
		gwlog.Panic(err)
	}

	srvtypemgr := aliveServicesByType[srvtype]
	if srvtypemgr == nil {
		srvtypemgr = newServiceTypeMgr()
		aliveServicesByType[srvtype] = srvtypemgr
	}

	srvtypemgr.registerService(srvid, registerInfo)
	gwlog.Infof("Service discoveried: %s.%s = %s", srvtype, srvid, registerInfo)
	delegate.OnServiceDiscovered(srvtype, srvid, registerInfo.Addr)
}

func handleDeleteServiceRegisterData(delegate ServiceDelegate, key []byte) {
	srvtype, srvid := parseRegisterPath(key)

	srvtypemgr := aliveServicesByType[srvtype]
	if srvtypemgr == nil {
		gwlog.Warnf("service %s.%s outdated, not not registered", srvtype, srvid)
		return
	}

	srvtypemgr.unregisterService(srvid)
	gwlog.Warnf("Service outdated: %s.%s", srvtype, srvid)
	delegate.OnServiceOutdated(srvtype, srvid)
}