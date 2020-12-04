package etcdhosts_client

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/mitchellh/go-homedir"
	"go.etcd.io/etcd/clientv3"
)

type HostsClient struct {
	hostKey string
	cli     *clientv3.Client
}

type VHosts struct {
	Version  int64
	Revision int64
	Hosts    string
}

type VHostsList []VHosts

func (v VHostsList) Swap(i, j int)      { v[i], v[j] = v[j], v[i] }
func (v VHostsList) Len() int           { return len(v) }
func (v VHostsList) Less(i, j int) bool { return v[i].Version > v[j].Version }

func NewClient(ca, cert, key string, endpoints []string, hostKey string) (*HostsClient, error) {
	if ca == "" || cert == "" || key == "" {
		return nil, errors.New("[etcd] certs config is empty")
	}

	if len(endpoints) < 1 {
		return nil, errors.New("[etcd] endpoints config is empty")
	}

	var caBs, certBs, keyBs []byte

	// if config is filepath, replace "~" to real home dir
	home, err := homedir.Dir()
	if err != nil {
		return nil, fmt.Errorf("[etcd] failed to get home dir: %w", err)
	}

	if strings.HasPrefix(ca, "~") {
		ca = strings.Replace(ca, "~", home, 1)
	}
	if strings.HasPrefix(cert, "~") {
		cert = strings.Replace(cert, "~", home, 1)
	}
	if strings.HasPrefix(key, "~") {
		key = strings.Replace(key, "~", home, 1)
	}

	// check config is base64 data or filepath
	_, err = os.Stat(ca)
	if err == nil {
		caBs, err = ioutil.ReadFile(ca)
		if err != nil {
			return nil, fmt.Errorf("[etcd/cert] read ca file %s failed: %w", ca, err)
		}
		certBs, err = ioutil.ReadFile(cert)
		if err != nil {
			return nil, fmt.Errorf("[etcd/cert] read cert file %s failed: %w", cert, err)
		}
		keyBs, err = ioutil.ReadFile(key)
		if err != nil {
			return nil, fmt.Errorf("[etcd/cert] read key file %s failed: %w", key, err)
		}
	} else {
		caBs, err = base64.StdEncoding.DecodeString(ca)
		if err != nil {
			return nil, fmt.Errorf("[etcd/cert] ca base64 decode failed: %w", err)
		}
		certBs, err = base64.StdEncoding.DecodeString(cert)
		if err != nil {
			return nil, fmt.Errorf("[etcd/cert] cert base64 decode failed: %w", err)
		}
		keyBs, err = base64.StdEncoding.DecodeString(key)
		if err != nil {
			return nil, fmt.Errorf("[etcd/cert] key base64 decode failed: %w", err)
		}
	}

	rootCertPool := x509.NewCertPool()
	rootCertPool.AppendCertsFromPEM(caBs)

	etcdClientCert, err := tls.X509KeyPair(certBs, keyBs)
	if err != nil {
		return nil, fmt.Errorf("[etcd/cert] x509 error: %w", err)
	}

	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   endpoints,
		DialTimeout: 5 * time.Second,
		TLS: &tls.Config{
			RootCAs:      rootCertPool,
			Certificates: []tls.Certificate{etcdClientCert},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("[etcd/client] create etcd client failed: %w", err)
	}
	return &HostsClient{
		hostKey: hostKey,
		cli:     cli,
	}, nil
}

func (hc *HostsClient) PutHosts(hosts string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err := hc.cli.Put(ctx, hc.hostKey, hosts)
	if err != nil {
		return fmt.Errorf("[etcd/client/put] push hosts failed, key %s: %w", hc.hostKey, err)
	}
	return nil
}

func (hc *HostsClient) GetHosts() (string, error) {
	return hc.GetHostsWithRevision(-1)
}

func (hc *HostsClient) GetHostsWithRevision(revision int64) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var resp *clientv3.GetResponse
	var err error
	if revision > -1 {
		resp, err = hc.cli.Get(ctx, hc.hostKey, clientv3.WithRev(revision))
	} else {
		resp, err = hc.cli.Get(ctx, hc.hostKey)
	}

	if err != nil {
		return "", fmt.Errorf("[etcd/client/get] get hosts failed, key %s: %w", hc.hostKey, err)
	}

	if len(resp.Kvs) == 0 {
		return "", fmt.Errorf("[etcd/client/get] etcd hosts not exist, key: %s", hc.hostKey)
	}

	if len(resp.Kvs) > 1 {
		return "", fmt.Errorf("[etcd/client/get] too many etcd hosts, key: %s", hc.hostKey)
	}

	return string(resp.Kvs[0].Value), nil
}

func (hc *HostsClient) GetHostsHistory() (VHostsList, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	getResp, err := hc.cli.Get(ctx, hc.hostKey)
	if err != nil {
		return nil, fmt.Errorf("[etcd/client/get] get hosts failed, key %s: %w", hc.hostKey, err)
	}
	if len(getResp.Kvs) < 1 {
		return nil, fmt.Errorf("[etcd/client/get] kvs not found, key %s", hc.hostKey)
	}

	vl := VHostsList{}
	for i := getResp.Header.Revision; i > 0; i-- {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		resp, err := hc.cli.Get(ctx, hc.hostKey, clientv3.WithRev(i))
		if err != nil {
			break
		}
		vl = append(vl, VHosts{
			Version:  resp.Kvs[0].Version,
			Revision: i,
			Hosts:    string(resp.Kvs[0].Value),
		})
		cancel()
	}
	sort.Sort(vl)
	return vl, nil
}
