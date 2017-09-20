package e2e

import (
	"reflect"
	"testing"

	"github.com/coreos-inc/vault-operator/pkg/util/k8sutil"
	"github.com/coreos-inc/vault-operator/test/e2e/e2eutil"
	"github.com/coreos-inc/vault-operator/test/e2e/framework"

	vaultapi "github.com/hashicorp/vault/api"
)

func TestCreateHAVault(t *testing.T) {
	f := framework.Global
	testVault, err := e2eutil.CreateCluster(t, f.VaultsCRClient, e2eutil.NewCluster("test-vault-", f.Namespace, 2))
	if err != nil {
		t.Fatalf("failed to create vault cluster: %v", err)
	}
	defer func() {
		if err := e2eutil.DeleteCluster(t, f.VaultsCRClient, testVault); err != nil {
			t.Fatalf("failed to delete vault cluster: %v", err)
		}
	}()

	vault, err := e2eutil.WaitAvailableVaultsUp(t, f.VaultsCRClient, 2, 6, testVault)
	if err != nil {
		t.Fatalf("failed to wait for cluster nodes to become available: %v", err)
	}

	tlsConfig, err := k8sutil.VaultTLSFromSecret(f.KubeClient, vault)
	if err != nil {
		t.Fatalf("failed to read TLS config for vault client: %v", err)
	}

	conns, err := e2eutil.PortForwardVaultClients(f.KubeClient, f.Config, f.Namespace, tlsConfig, vault.Status.AvailableNodes...)
	if err != nil {
		t.Fatalf("failed to portforward and create vault clients: %v", err)
	}
	defer e2eutil.CleanupConnections(t, f.Namespace, conns)

	initOpts := &vaultapi.InitRequest{
		SecretShares:    1,
		SecretThreshold: 1,
	}

	// Init vault via the first available node
	podName := vault.Status.AvailableNodes[0]
	conn, ok := conns[podName]
	if !ok {
		t.Fatalf("failed to find vault client for pod (%v)", podName)
	}
	initResp, err := conn.VClient.Sys().Init(initOpts)
	if err != nil {
		t.Fatalf("failed to initialize vault: %v", err)
	}

	vault, err = e2eutil.WaitSealedVaultsUp(t, f.VaultsCRClient, 2, 6, testVault)
	if err != nil {
		t.Fatalf("failed to wait for vault nodes to become sealed: %v", err)
	}

	// Unseal the 1st vault node and wait for it to become active
	unsealResp, err := conn.VClient.Sys().Unseal(initResp.Keys[0])
	if err != nil {
		t.Fatalf("failed to unseal vault: %v", err)
	}
	if unsealResp.Sealed {
		t.Fatal("failed to unseal vault: unseal response still shows vault as sealed")
	}
	vault, err = e2eutil.WaitActiveVaultsUp(t, f.VaultsCRClient, 6, testVault)
	if err != nil {
		t.Fatalf("failed to wait for any node to become active: %v", err)
	}

	// Unseal the 2nd vault node(the remaining sealed node) and wait for it to become standby
	podName = vault.Status.SealedNodes[0]
	conn, ok = conns[podName]
	if !ok {
		t.Fatalf("failed to find vault client for pod (%v)", podName)
	}
	unsealResp, err = conn.VClient.Sys().Unseal(initResp.Keys[0])
	if err != nil {
		t.Fatalf("failed to unseal vault: %v", err)
	}
	if unsealResp.Sealed {
		t.Fatal("failed to unseal vault: unseal response still shows vault as sealed")
	}
	vault, err = e2eutil.WaitStandbyVaultsUp(t, f.VaultsCRClient, 1, 6, testVault)
	if err != nil {
		t.Fatalf("failed to wait for vault nodes to become standby: %v", err)
	}

	// Write secret to active node
	podName = vault.Status.ActiveNode
	conn, ok = conns[podName]
	if !ok {
		t.Fatalf("failed to find vault client for pod (%v)", podName)
	}
	conn.VClient.SetToken(initResp.RootToken)

	path := "secret/login"
	data := &e2eutil.SampleSecret{Username: "user", Password: "pass"}
	secretData, err := e2eutil.MapObjectToArbitraryData(data)
	if err != nil {
		t.Fatalf("failed to create secret data: %v", err)
	}

	_, err = conn.VClient.Logical().Write(path, secretData)
	if err != nil {
		t.Fatalf("failed to write secret (%v) to vault node (%v): %v", path, podName, err)
	}

	// Read secret back from active node
	secret, err := conn.VClient.Logical().Read(path)
	if err != nil {
		t.Fatalf("failed to read secret(%v) from vault node (%v): %v", path, podName, err)
	}

	if !reflect.DeepEqual(secret.Data, secretData) {
		// TODO: Print out secrets
		t.Fatal("Read secret data is not the same as write secret")
	}

}
