package expose

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jenkins-x/jx/pkg/helm"
	"github.com/jenkins-x/jx/pkg/util"

	"github.com/jenkins-x/jx/pkg/certmanager"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/Pallinder/go-randomdata"
	"github.com/jenkins-x/jx/pkg/kube"
	"github.com/jenkins-x/jx/pkg/kube/services"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	exposecontroller        = "exposecontroller"
	exposecontrollerVersion = "2.3.82"
	exposecontrollerChart   = "jenkins-x/exposecontroller"
)

// Expose gets an existing config from the devNamespace and runs exposecontroller in the targetNamespace
func Expose(devNamespace, targetNamespace, password string, kubeClient kubernetes.Interface, helmer helm.Helmer, installTimeout string) error {
	// todo switch to using exposecontroller as a jx plugin
	_, err := kubeClient.CoreV1().Secrets(targetNamespace).Get(kube.SecretBasicAuth, metav1.GetOptions{})
	if err != nil {
		data := make(map[string][]byte)

		if password != "" {
			hash := util.HashPassword(password)
			data[kube.AUTH] = []byte(fmt.Sprintf("admin:{SHA}%s", hash))
		} else {
			basicAuth, err := kubeClient.CoreV1().Secrets(devNamespace).Get(kube.SecretBasicAuth, metav1.GetOptions{})
			if err != nil {
				return fmt.Errorf("cannot find secret %s in namespace %s: %v", kube.SecretBasicAuth, devNamespace, err)
			}
			data = basicAuth.Data
		}

		sec := &corev1.Secret{
			Data: data,
			ObjectMeta: metav1.ObjectMeta{
				Name: kube.SecretBasicAuth,
			},
		}
		_, err := kubeClient.CoreV1().Secrets(targetNamespace).Create(sec)
		if err != nil {
			return fmt.Errorf("cannot create secret %s in target namespace %s: %v", kube.SecretBasicAuth, targetNamespace, err)
		}
	}

	ic, err := kube.GetIngressConfig(kubeClient, devNamespace)
	if err != nil {
		return fmt.Errorf("cannot get existing team exposecontroller config from namespace %s: %v", devNamespace, err)
	}

	err = services.AnnotateNamespaceServicesWithCertManager(kubeClient, targetNamespace, ic.Issuer)
	if err != nil {
		return err
	}

	// if targetnamespace is different than dev check if there's any certmanager CRDs, if not check dev and copy any found across
	err = certmanager.CopyCertmanagerResources(targetNamespace, ic, kubeClient)
	if err != nil {
		return fmt.Errorf("failed to copy certmanager resources from %s to %s namespace: %v", devNamespace, targetNamespace, err)
	}

	return RunExposecontroller(devNamespace, targetNamespace, ic, kubeClient, helmer, installTimeout)
}

// RunExposecontroller executes the ExposeController as a Job in the targetNamespace for the ingressConfig in ic
// using the kubeClient and helmer interfaces, and respecting the installTimeout.
// Additional services to expose can be specified.
func RunExposecontroller(devNamespace, targetNamespace string, ic kube.IngressConfig,
	kubeClient kubernetes.Interface, helmer helm.Helmer, installTimeout string, services ...string) error {

	CleanExposecontrollerReources(kubeClient, targetNamespace)

	exValues := []string{
		"config.exposer=" + ic.Exposer,
		"config.domain=" + ic.Domain,
		"config.tlsacme=" + strconv.FormatBool(ic.TLS),
	}

	if !ic.TLS && ic.Issuer != "" {
		exValues = append(exValues, "config.http=true")
	}

	if len(services) > 0 {
		serviceCfg := "config.extravalues.services={"
		for i, service := range services {
			if i > 0 {
				serviceCfg += ","
			}
			serviceCfg += service
		}
		serviceCfg += "}"
		exValues = append(exValues, serviceCfg)
	}

	helmRelease := "expose-" + strings.ToLower(randomdata.SillyName())
	err := helm.InstallFromChartOptions(helm.InstallChartOptions{
		ReleaseName: helmRelease,
		Chart:       exposecontrollerChart,
		Version:     exposecontrollerVersion,
		Ns:          targetNamespace,
		HelmUpdate:  true,
		SetValues:   exValues,
	}, helmer, kubeClient, installTimeout)
	if err != nil {
		return fmt.Errorf("exposecontroller deployment failed: %v", err)
	}
	err = kube.WaitForJobToSucceeded(kubeClient, targetNamespace, exposecontroller, 5*time.Minute)
	if err != nil {
		return fmt.Errorf("failed waiting for exposecontroller job to succeed: %v", err)
	}
	return helmer.DeleteRelease(targetNamespace, helmRelease, true)

}

// CleanExposecontrollerReources cleans expose controller resources
func CleanExposecontrollerReources(kubeClient kubernetes.Interface, ns string) {
	// let's not error if nothing to cleanup
	kubeClient.RbacV1().Roles(ns).Delete(exposecontroller, &metav1.DeleteOptions{})
	kubeClient.RbacV1().RoleBindings(ns).Delete(exposecontroller, &metav1.DeleteOptions{})
	kubeClient.RbacV1().ClusterRoleBindings().Delete(exposecontroller, &metav1.DeleteOptions{})
	kubeClient.CoreV1().ConfigMaps(ns).Delete(exposecontroller, &metav1.DeleteOptions{})
	kubeClient.CoreV1().ServiceAccounts(ns).Delete(exposecontroller, &metav1.DeleteOptions{})
	kubeClient.BatchV1().Jobs(ns).Delete(exposecontroller, &metav1.DeleteOptions{})
}