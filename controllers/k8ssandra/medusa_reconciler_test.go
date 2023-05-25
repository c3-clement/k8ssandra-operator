package k8ssandra

import (
	"context"
	"fmt"
	"testing"

	cassdcapi "github.com/k8ssandra/cass-operator/apis/cassandra/v1beta1"
	api "github.com/k8ssandra/k8ssandra-operator/apis/k8ssandra/v1alpha1"
	medusaapi "github.com/k8ssandra/k8ssandra-operator/apis/medusa/v1alpha1"
	cassandra "github.com/k8ssandra/k8ssandra-operator/pkg/cassandra"
	"github.com/k8ssandra/k8ssandra-operator/pkg/images"
	medusa "github.com/k8ssandra/k8ssandra-operator/pkg/medusa"
	"github.com/k8ssandra/k8ssandra-operator/pkg/utils"
	"github.com/k8ssandra/k8ssandra-operator/test/framework"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	medusaImageRepo     = "test"
	storageSecret       = "storage-secret"
	cassandraUserSecret = "medusa-secret"
)

func createMultiDcClusterWithMedusa(t *testing.T, ctx context.Context, f *framework.Framework, namespace string) {
	require := require.New(t)

	kc := &api.K8ssandraCluster{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      "test",
		},
		Spec: api.K8ssandraClusterSpec{
			Cassandra: &api.CassandraClusterTemplate{
				Datacenters: []api.CassandraDatacenterTemplate{
					{
						Meta: api.EmbeddedObjectMeta{
							Name: "dc1",
						},
						K8sContext: f.DataPlaneContexts[0],
						Size:       3,
						DatacenterOptions: api.DatacenterOptions{
							ServerVersion: "3.11.14",
							StorageConfig: &cassdcapi.StorageConfig{
								CassandraDataVolumeClaimSpec: &corev1.PersistentVolumeClaimSpec{
									StorageClassName: &defaultStorageClass,
								},
							},
						},
					},
					{
						Meta: api.EmbeddedObjectMeta{
							Name: "dc2",
						},
						K8sContext: f.DataPlaneContexts[1],
						Size:       3,
						DatacenterOptions: api.DatacenterOptions{
							ServerVersion: "3.11.14",
							StorageConfig: &cassdcapi.StorageConfig{
								CassandraDataVolumeClaimSpec: &corev1.PersistentVolumeClaimSpec{
									StorageClassName: &defaultStorageClass,
								},
							},
						},
					},
				},
			},
			Medusa: &medusaapi.MedusaClusterTemplate{
				ContainerImage: &images.Image{
					Repository: medusaImageRepo,
				},
				StorageProperties: medusaapi.Storage{
					StorageSecretRef: corev1.LocalObjectReference{
						Name: cassandraUserSecret,
					},
				},
				CassandraUserSecretRef: corev1.LocalObjectReference{
					Name: cassandraUserSecret,
				},
				ReadinessProbe: &corev1.Probe{
					InitialDelaySeconds: 1,
					TimeoutSeconds:      2,
					PeriodSeconds:       3,
					SuccessThreshold:    1,
					FailureThreshold:    5,
				},
				LivenessProbe: &corev1.Probe{
					InitialDelaySeconds: 6,
					TimeoutSeconds:      7,
					PeriodSeconds:       8,
					SuccessThreshold:    1,
					FailureThreshold:    10,
				},
				Resources: &corev1.ResourceRequirements{
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("150m"),
						corev1.ResourceMemory: resource.MustParse("500Mi"),
					},
				},
			},
		},
	}

	t.Log("Creating k8ssandracluster with Medusa")
	err := f.Client.Create(ctx, kc)
	require.NoError(err, "failed to create K8ssandraCluster")
	verifyReplicatedSecretReconciled(ctx, t, f, kc)

	reconcileMedusaStandaloneDeployment(ctx, t, f, kc, "dc1", f.DataPlaneContexts[0])
	t.Log("check that dc1 was created")
	dc1Key := framework.ClusterKey{NamespacedName: types.NamespacedName{Namespace: namespace, Name: "dc1"}, K8sContext: f.DataPlaneContexts[0]}
	require.Eventually(f.DatacenterExists(ctx, dc1Key), timeout, interval)

	verifySecretAnnotationAdded(t, f, ctx, dc1Key, cassandraUserSecret)

	t.Log("check that the standalone Medusa deployment was created in dc1")
	medusaDeploymentKey1 := framework.ClusterKey{NamespacedName: types.NamespacedName{Namespace: namespace, Name: medusa.MedusaStandaloneDeploymentName("test", "dc1")}, K8sContext: f.DataPlaneContexts[0]}
	medusaDeployment1 := &appsv1.Deployment{}
	require.Eventually(func() bool {
		if err := f.Get(ctx, medusaDeploymentKey1, medusaDeployment1); err != nil {
			return false
		}
		return true
	}, timeout, interval)

	require.True(f.ContainerHasEnvVar(medusaDeployment1.Spec.Template.Spec.Containers[0], "MEDUSA_RESOLVE_IP_ADDRESSES", "False"))

	t.Log("check that the standalone Medusa service was created")
	medusaServiceKey1 := framework.ClusterKey{NamespacedName: types.NamespacedName{Namespace: namespace, Name: medusa.MedusaServiceName("test", "dc1")}, K8sContext: f.DataPlaneContexts[0]}
	medusaService1 := &corev1.Service{}
	require.Eventually(func() bool {
		if err := f.Get(ctx, medusaServiceKey1, medusaService1); err != nil {
			return false
		}
		return true
	}, timeout, interval)

	t.Log("update datacenter status to scaling up")
	err = f.PatchDatacenterStatus(ctx, dc1Key, func(dc *cassdcapi.CassandraDatacenter) {
		dc.SetCondition(cassdcapi.DatacenterCondition{
			Type:               cassdcapi.DatacenterScalingUp,
			Status:             corev1.ConditionTrue,
			LastTransitionTime: metav1.Now(),
		})
	})
	require.NoError(err, "failed to patch datacenter status")

	kcKey := framework.ClusterKey{K8sContext: f.ControlPlaneContext, NamespacedName: types.NamespacedName{Namespace: namespace, Name: "test"}}

	t.Log("check that the K8ssandraCluster status is updated")
	require.Eventually(func() bool {
		kc := &api.K8ssandraCluster{}
		err = f.Get(ctx, kcKey, kc)

		if err != nil {
			t.Logf("failed to get K8ssandraCluster: %v", err)
			return false
		}

		if len(kc.Status.Datacenters) == 0 {
			return false
		}

		k8ssandraStatus, found := kc.Status.Datacenters[dc1Key.Name]
		if !found {
			t.Logf("status for datacenter %s not found", dc1Key)
			return false
		}

		condition := FindDatacenterCondition(k8ssandraStatus.Cassandra, cassdcapi.DatacenterScalingUp)
		return !(condition == nil && condition.Status == corev1.ConditionFalse)
	}, timeout, interval, "timed out waiting for K8ssandraCluster status update")

	dc1 := &cassdcapi.CassandraDatacenter{}
	err = f.Get(ctx, dc1Key, dc1)
	checkMedusaObjectsCompliance(t, f, dc1, kc)

	t.Log("check that dc2 has not been created yet")
	dc2Key := framework.ClusterKey{NamespacedName: types.NamespacedName{Namespace: namespace, Name: "dc2"}, K8sContext: f.DataPlaneContexts[1]}
	dc2 := &cassdcapi.CassandraDatacenter{}
	err = f.Get(ctx, dc2Key, dc2)
	require.True(err != nil && errors.IsNotFound(err), "dc2 should not be created until dc1 is ready")

	t.Log("update dc1 status to ready")
	err = f.PatchDatacenterStatus(ctx, dc1Key, func(dc *cassdcapi.CassandraDatacenter) {
		dc.Status.CassandraOperatorProgress = cassdcapi.ProgressReady
		dc.SetCondition(cassdcapi.DatacenterCondition{
			Type:               cassdcapi.DatacenterReady,
			Status:             corev1.ConditionTrue,
			LastTransitionTime: metav1.Now(),
		})
	})
	require.NoError(err, "failed to update dc1 status to ready")

	require.Eventually(func() bool {
		return f.UpdateDatacenterGeneration(ctx, t, dc1Key)
	}, timeout, interval, "failed to update dc1 generation")

	reconcileMedusaStandaloneDeployment(ctx, t, f, kc, "dc2", f.DataPlaneContexts[1])
	t.Log("check that dc2 was created")
	require.Eventually(f.DatacenterExists(ctx, dc2Key), timeout, interval)

	t.Log("check that the standalone Medusa deployment was created in dc2")
	medusaDeploymentKey2 := framework.ClusterKey{NamespacedName: types.NamespacedName{Namespace: namespace, Name: medusa.MedusaStandaloneDeploymentName("test", "dc2")}, K8sContext: f.DataPlaneContexts[1]}
	medusaDeployment2 := &appsv1.Deployment{}
	require.Eventually(func() bool {
		if err := f.Get(ctx, medusaDeploymentKey2, medusaDeployment2); err != nil {
			return false
		}
		return true
	}, timeout, interval)

	t.Log("check that the standalone Medusa service was created in dc2")
	medusaServiceKey2 := framework.ClusterKey{NamespacedName: types.NamespacedName{Namespace: namespace, Name: medusa.MedusaServiceName("test", "dc2")}, K8sContext: f.DataPlaneContexts[1]}
	medusaService2 := &corev1.Service{}
	require.Eventually(func() bool {
		if err := f.Get(ctx, medusaServiceKey2, medusaService2); err != nil {
			return false
		}
		return true
	}, timeout, interval)

	t.Log("check that remote seeds are set on dc2")
	dc2 = &cassdcapi.CassandraDatacenter{}
	err = f.Get(ctx, dc2Key, dc2)
	require.NoError(err, "failed to get dc2")

	t.Log("update dc2 status to ready")
	err = f.PatchDatacenterStatus(ctx, dc2Key, func(dc *cassdcapi.CassandraDatacenter) {
		dc.Status.CassandraOperatorProgress = cassdcapi.ProgressReady
		dc.SetCondition(cassdcapi.DatacenterCondition{
			Type:               cassdcapi.DatacenterReady,
			Status:             corev1.ConditionTrue,
			LastTransitionTime: metav1.Now(),
		})
	})
	require.NoError(err, "failed to update dc2 status to ready")

	require.Eventually(func() bool {
		return f.UpdateDatacenterGeneration(ctx, t, dc2Key)
	}, timeout, interval, "failed to update dc2 generation")

	t.Log("check that dc2 was rebuilt")
	verifyRebuildTaskCreated(ctx, t, f, dc2Key, dc1Key)
	rebuildTaskKey := framework.NewClusterKey(f.DataPlaneContexts[1], kc.Namespace, "dc2-rebuild")
	setRebuildTaskFinished(ctx, t, f, rebuildTaskKey, dc2Key)

	checkMedusaObjectsCompliance(t, f, dc2, kc)
	verifySecretAnnotationAdded(t, f, ctx, dc2Key, cassandraUserSecret)

	t.Log("check that the K8ssandraCluster status is updated")
	require.Eventually(func() bool {
		kc := &api.K8ssandraCluster{}
		err = f.Get(ctx, kcKey, kc)
		if err != nil {
			t.Logf("failed to get K8ssandraCluster: %v", err)
			return false
		}

		if len(kc.Status.Datacenters) != 2 {
			return false
		}

		k8ssandraStatus, found := kc.Status.Datacenters[dc1Key.Name]
		if !found {
			t.Logf("status for datacenter %s not found", dc1Key)
			return false
		}

		condition := FindDatacenterCondition(k8ssandraStatus.Cassandra, cassdcapi.DatacenterReady)
		if condition == nil || condition.Status == corev1.ConditionFalse {
			t.Logf("k8ssandracluster status check failed: cassandra in %s is not ready", dc1Key.Name)
			return false
		}

		k8ssandraStatus, found = kc.Status.Datacenters[dc2Key.Name]
		if !found {
			t.Logf("status for datacenter %s not found", dc2Key)
			return false
		}

		condition = FindDatacenterCondition(k8ssandraStatus.Cassandra, cassdcapi.DatacenterReady)
		if condition == nil || condition.Status == corev1.ConditionFalse {
			t.Logf("k8ssandracluster status check failed: cassandra in %s is not ready", dc2Key.Name)
			return false
		}

		return true
	}, timeout, interval, "timed out waiting for K8ssandraCluster status update")

	// Test cluster deletion
	t.Log("deleting K8ssandraCluster")
	err = f.DeleteK8ssandraCluster(ctx, client.ObjectKey{Namespace: namespace, Name: kc.Name}, timeout, interval)
	require.NoError(err, "failed to delete K8ssandraCluster")
	f.AssertObjectDoesNotExist(ctx, t, dc1Key, &cassdcapi.CassandraDatacenter{}, timeout, interval)
	// Check that Medusa Standalone deployment and service were deleted
	f.AssertObjectDoesNotExist(ctx, t, medusaDeploymentKey1, &appsv1.Deployment{}, timeout, interval)
	f.AssertObjectDoesNotExist(ctx, t, medusaDeploymentKey2, &appsv1.Deployment{}, timeout, interval)
	f.AssertObjectDoesNotExist(ctx, t, medusaServiceKey1, &corev1.Service{}, timeout, interval)
	f.AssertObjectDoesNotExist(ctx, t, medusaServiceKey2, &corev1.Service{}, timeout, interval)
}

// Check that all the Medusa related objects have been created and are in the expected state.
func checkMedusaObjectsCompliance(t *testing.T, f *framework.Framework, dc *cassdcapi.CassandraDatacenter, kc *api.K8ssandraCluster) {
	require := require.New(t)

	// Check containers presence
	initContainerIndex, found := cassandra.FindInitContainer(dc.Spec.PodTemplateSpec, "medusa-restore")
	require.True(found, fmt.Sprintf("%s doesn't have medusa-restore init container", dc.Name))
	_, foundConfig := cassandra.FindInitContainer(dc.Spec.PodTemplateSpec, "server-config-init")
	require.True(foundConfig, fmt.Sprintf("%s doesn't have server-config-init container", dc.Name))
	initContainer := dc.Spec.PodTemplateSpec.Spec.InitContainers[initContainerIndex]
	containerIndex, found := cassandra.FindContainer(dc.Spec.PodTemplateSpec, "medusa")
	require.True(found, fmt.Sprintf("%s doesn't have medusa container", dc.Name))
	mainContainer := dc.Spec.PodTemplateSpec.Spec.Containers[containerIndex]

	for _, container := range [](corev1.Container){initContainer, mainContainer} {
		// Check containers Image
		require.True(container.Image == fmt.Sprintf("docker.io/%s/medusa:latest", medusaImageRepo), fmt.Sprintf("%s %s init container doesn't have the right image %s vs docker.io/%s/medusa:latest", dc.Name, container.Name, container.Image, medusaImageRepo))

		// Check volume mounts
		assert.True(t, f.ContainerHasVolumeMount(container, "server-config", "/etc/cassandra"), "Missing Volume Mount for medusa-restore server-config")
		assert.True(t, f.ContainerHasVolumeMount(container, "server-data", "/var/lib/cassandra"), "Missing Volume Mount for medusa-restore server-data")
		assert.True(t, f.ContainerHasVolumeMount(container, "podinfo", "/etc/podinfo"), "Missing Volume Mount for medusa-restore podinfo")
		assert.True(t, f.ContainerHasVolumeMount(container, cassandraUserSecret, "/etc/medusa-secrets"), "Missing Volume Mount for medusa-restore medusa-secrets")
		assert.True(t, f.ContainerHasVolumeMount(container, fmt.Sprintf("%s-medusa", kc.Name), "/etc/medusa"), "Missing Volume Mount for medusa-restore medusa config")

		// Check env vars
		if container.Name == "medusa" {
			assert.True(t, f.ContainerHasEnvVar(container, "MEDUSA_MODE", "GRPC"), "Wrong MEDUSA_MODE env var for medusa")
		} else {
			assert.True(t, f.ContainerHasEnvVar(container, "MEDUSA_MODE", "RESTORE"), "Wrong MEDUSA_MODE env var for medusa-restore")
		}
		assert.True(t, f.ContainerHasEnvVar(container, "MEDUSA_TMP_DIR", ""), "Missing MEDUSA_TMP_DIR env var for medusa-restore")
	}
}

func reconcileMedusaStandaloneDeployment(ctx context.Context, t *testing.T, f *framework.Framework, kc *api.K8ssandraCluster, dcName string, k8sContext string) {
	t.Log("check ReplicatedSecret reconciled")

	medusaDepl := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      medusa.MedusaStandaloneDeploymentName(kc.SanitizedName(), dcName),
			Namespace: kc.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": medusa.MedusaStandaloneDeploymentName(kc.SanitizedName(), dcName)},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": medusa.MedusaStandaloneDeploymentName(kc.SanitizedName(), dcName)},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  medusa.MedusaStandaloneDeploymentName(kc.SanitizedName(), dcName),
							Image: "quay.io/k8ssandra/medusa:0.11.0",
						},
					},
				},
			},
		},
	}
	medusaKey := framework.ClusterKey{NamespacedName: utils.GetKey(medusaDepl), K8sContext: k8sContext}
	f.Create(ctx, medusaKey, medusaDepl)

	actualMedusaDepl := &appsv1.Deployment{}
	assert.Eventually(t, func() bool {
		err := f.Get(ctx, medusaKey, actualMedusaDepl)
		return err == nil
	}, timeout, interval, "failed to get Medusa Deployment")

	err := f.SetMedusaDeplReadyReplicas(ctx, medusaKey)

	require.NoError(t, err, "Failed to update Medusa Deployment status")
}
