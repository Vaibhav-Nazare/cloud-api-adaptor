package e2e

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/e2e-framework/klient"
	"sigs.k8s.io/e2e-framework/klient/k8s"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/klient/wait/conditions"
	"sigs.k8s.io/e2e-framework/pkg/env"
)

const WAIT_NAMESPACE_AVAILABLE_TIMEOUT = time.Second * 120

func reverseSlice(slice []string) []string {
	length := len(slice)
	for i := 0; i < length/2; i++ {
		slice[i], slice[length-i-1] = slice[length-i-1], slice[i]
	}
	return slice
}

// timeExtractor for comparing and extracting time from a Log String
func timeExtractor(log string) (string, error) {
	matchString := regexp.MustCompile(`\b(\d{2}):(\d{2}):(\d{2})\b`).FindStringSubmatch(log)
	if len(matchString) != 4 {
		return "", errors.New("Invalid Time Data")
	}
	return matchString[0], nil
}

func NewTestCase(t *testing.T, e env.Environment, testName string, assert CloudAssert, assessMessage string) *TestCase {
	testCase := &TestCase{
		testing:        t,
		testEnv:        e,
		testName:       testName,
		assert:         assert,
		assessMessage:  assessMessage,
		podState:       v1.PodRunning,
		imagePullTimer: false,
		deletionWithin: assert.DefaultTimeout(),
	}

	return testCase
}

func NewExtraPod(namespace string, podName string, containerName string, imageName string, options ...PodOption) *ExtraPod {
	basicPod := NewPod(namespace, podName, containerName, imageName)
	for _, option := range options {
		option(basicPod)
	}
	extPod := &ExtraPod{
		pod:      basicPod,
		podState: v1.PodRunning,
	}
	return extPod
}

func WatchImagePullTime(ctx context.Context, client klient.Client, caaPod v1.Pod, pod v1.Pod) (string, error) {
	pullingtime := ""
	var startTime, endTime time.Time

	if pod.Status.Phase == v1.PodRunning {
		podLogString, err := GetPodLog(ctx, client, caaPod)
		if err != nil {
			return "", err
		}

		if podLogString != "" {
			podLogSlice := reverseSlice(strings.Split(podLogString, "\n"))
			for _, i := range podLogSlice {
				if strings.Contains(i, "calling PullImage for \""+pod.Spec.Containers[0].Image+"\"") {
					timeString, err := timeExtractor(i)
					if err != nil {
						return "", err
					}
					startTime, err = time.Parse("15:04:05", timeString)
					if err != nil {
						return "", err
					}
					break
				}
				if strings.Contains(i, "successfully pulled image \""+pod.Spec.Containers[0].Image+"\"") {
					timeString, err := timeExtractor(i)
					if err != nil {
						return "", err
					}
					endTime, err = time.Parse("15:04:05", timeString)
					if err != nil {
						return "", err
					}
				}
			}
		} else {
			return "", errors.New("Pod Failed to Log expected Output")
		}
	} else {
		return "", errors.New("Pod Failed to Start")
	}

	pullingtime = endTime.Sub(startTime).String()
	return pullingtime, nil
}

// Check cloud-api-adaptor daemonset pod logs to ensure that something like:
// <date time> [adaptor/proxy]         mount_point:/run/kata-containers/<id>/rootfs source:<image> fstype:overlay driver:image_guest_pull
// <date time> 11:47:42 [adaptor/proxy] CreateContainer: Ignoring PullImage before CreateContainer (cid: "<cid>")
// was output
func IsPulledWithNydusSnapshotter(ctx context.Context, t *testing.T, client klient.Client, nodeName string, containerId string) (bool, error) {
	var podlist v1.PodList

	nydusSnapshotterPullRegex, err := regexp.Compile(`.*mount_point:/run/kata-containers.*` + containerId + `.*driver:image_guest_pull.*$`)
	if err != nil {
		return false, err
	}

	if err := client.Resources("confidential-containers-system").List(ctx, &podlist); err != nil {
		t.Fatal(err)
	}
	for _, pod := range podlist.Items {
		if pod.Labels["app"] == "cloud-api-adaptor" && pod.Spec.NodeName == nodeName {
			podLogString, err := GetPodLog(ctx, client, pod)
			if err != nil {
				return false, err
			}

			podLogSlice := reverseSlice(strings.Split(podLogString, "\n"))
			for _, line := range podLogSlice {
				if nydusSnapshotterPullRegex.MatchString(line) {
					return true, nil
				}
			}
			return false, fmt.Errorf("Didn't find pull image for snapshotter")
		}
	}
	return false, fmt.Errorf("No cloud-api-adaptor pod found in podList: %v", podlist.Items)
}

func GetPodLog(ctx context.Context, client klient.Client, pod v1.Pod) (string, error) {
	clientset, err := kubernetes.NewForConfig(client.RESTConfig())
	if err != nil {
		return "", err
	}

	req := clientset.CoreV1().Pods(pod.ObjectMeta.Namespace).GetLogs(pod.ObjectMeta.Name, &v1.PodLogOptions{})
	podLogs, err := req.Stream(ctx)
	if err != nil {
		return "", err
	}
	defer podLogs.Close()
	buf := new(bytes.Buffer)
	_, err = io.Copy(buf, podLogs)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(buf.String()), nil
}

func ComparePodLogString(ctx context.Context, client klient.Client, customPod v1.Pod, expectedPodLogString string) (string, error) {
	//adding sleep time to initialize container and ready for logging
	time.Sleep(5 * time.Second)

	podLogString, err := getStringFromPod(ctx, client, customPod, GetPodLog)
	if err != nil {
		return "", err
	}

	if !strings.Contains(podLogString, expectedPodLogString) {
		return podLogString, errors.New("Error: Pod Log doesn't contain Expected String")
	}

	return podLogString, nil
}

// Note: there are currently two event types: Normal and Warning, so Warning includes errors
func GetPodEventWarningDescriptions(ctx context.Context, client klient.Client, pod v1.Pod) (string, error) {
	clientset, err := kubernetes.NewForConfig(client.RESTConfig())
	if err != nil {
		return "", err
	}

	events, err := clientset.CoreV1().Events(pod.Namespace).List(ctx, metav1.ListOptions{FieldSelector: fmt.Sprintf("involvedObject.name=%s", pod.Name)})
	if err != nil {
		return "", err
	}

	var descriptionsBuilder strings.Builder
	for _, event := range events.Items {
		if event.Type == v1.EventTypeWarning {
			descriptionsBuilder.WriteString(event.Message)
		}
	}
	return descriptionsBuilder.String(), nil
}

// This function takes an expected pod event "warning" string (note warning also covers errors) and checks to see if it
// shows up in the event log of the pod. Some pods error in failed state, so can be immediately checks, others fail
// in waiting state (e.g. ImagePullBackoff errors), so we need to poll for errors showing up on these pods
func ComparePodEventWarningDescriptions(ctx context.Context, t *testing.T, client klient.Client, pod v1.Pod, expectedPodEvent string) error {
	retries := 1
	delay := 10 * time.Second

	if pod.Status.Phase != v1.PodFailed {
		// If not failed state we might have to wait/retry until the error happens
		retries = int(WAIT_POD_RUNNING_TIMEOUT / delay)
	}

	var err error = nil
	for retries > 0 {
		podEventsDescriptions, podErr := getStringFromPod(ctx, client, pod, GetPodEventWarningDescriptions)
		if podErr != nil {
			return podErr
		}

		t.Logf("podEvents: %s\n", podEventsDescriptions)
		if !strings.Contains(podEventsDescriptions, expectedPodEvent) {
			err = fmt.Errorf("error: Pod Events don't contain Expected String %s", expectedPodEvent)
		} else {
			return nil
		}
		retries--
		time.Sleep(delay)
	}
	return err
}

func CompareInstanceType(ctx context.Context, t *testing.T, client klient.Client, pod v1.Pod, expectedInstanceType string, getInstanceTypeFn func(t *testing.T, podName string) (string, error)) error {
	var podlist v1.PodList
	if err := client.Resources(pod.Namespace).List(ctx, &podlist); err != nil {
		return err
	}
	for _, podItem := range podlist.Items {
		if podItem.ObjectMeta.Name == pod.Name {
			instanceType, err := getInstanceTypeFn(t, pod.Name)
			if err != nil {
				t.Fatal(err)
			}
			if instanceType == expectedInstanceType {
				return nil
			} else {
				return fmt.Errorf("error: Pod instance type was %s, but we expected %s ", instanceType, expectedInstanceType)
			}
		}
	}
	return fmt.Errorf("no pod matching %v, was found", pod)
}

func VerifyAlternateImage(ctx context.Context, t *testing.T, client klient.Client, alternateImageName string) error {
	var caaPod v1.Pod
	caaPod.Namespace = "confidential-containers-system"
	expectedSuccessMessage := "Choosing " + alternateImageName

	pods, err := GetPodNamesByLabel(ctx, client, t, caaPod.Namespace, "app", "cloud-api-adaptor")
	if err != nil {
		t.Fatal(err)
	}

	caaPod.Name = pods.Items[0].Name
	LogString, err := ComparePodLogString(ctx, client, caaPod, expectedSuccessMessage)
	if err != nil {
		t.Logf("Output:%s", LogString)
		t.Fatal(err)
	}
	t.Logf("PodVM was brought up using the alternate PodVM image %s", alternateImageName)
	return nil
}

func GetNodeNameFromPod(ctx context.Context, client klient.Client, customPod v1.Pod) (string, error) {
	var getNodeName = func(ctx context.Context, client klient.Client, pod v1.Pod) (string, error) {
		return pod.Spec.NodeName, nil
	}
	return getStringFromPod(ctx, client, customPod, getNodeName)
}

func GetSuccessfulAndErroredPods(ctx context.Context, t *testing.T, client klient.Client, job batchv1.Job) (int, int, string, error) {
	podLogString := ""
	errorPod := 0
	successPod := 0
	var podlist v1.PodList
	clientset, err := kubernetes.NewForConfig(client.RESTConfig())
	if err != nil {
		return 0, 0, "", err
	}
	if err := client.Resources(job.Namespace).List(ctx, &podlist); err != nil {
		return 0, 0, "", err
	}
	for _, pod := range podlist.Items {
		if pod.ObjectMeta.Labels["job-name"] != job.Name {
			continue
		}
		if pod.Status.Phase == v1.PodPending {
			if pod.Status.ContainerStatuses[0].State.Waiting.Reason == "ContainerCreating" {
				return 0, 0, "", errors.New("Failed to Create PodVM")
			}
		}
		if pod.Status.ContainerStatuses[0].State.Terminated.Reason == "StartError" {
			errorPod++
			t.Log("WARNING:", pod.ObjectMeta.Name, "-", pod.Status.ContainerStatuses[0].State.Terminated.Reason)
		}
		if pod.Status.ContainerStatuses[0].State.Terminated.Reason == "Completed" {
			successPod++
			watcher, err := clientset.CoreV1().Events(job.Namespace).Watch(ctx, metav1.ListOptions{})
			if err != nil {
				return 0, 0, "", err
			}
			defer watcher.Stop()
			for event := range watcher.ResultChan() {
				if event.Object.(*v1.Event).Reason == "Started" && pod.Status.ContainerStatuses[0].State.Terminated.Reason == "Completed" {
					func() {
						req := clientset.CoreV1().Pods(job.Namespace).GetLogs(pod.ObjectMeta.Name, &v1.PodLogOptions{})
						podLogs, err := req.Stream(ctx)
						if err != nil {
							return
						}
						defer podLogs.Close()
						buf := new(bytes.Buffer)
						_, err = io.Copy(buf, podLogs)
						if err != nil {
							return
						}
						podLogString = strings.TrimSpace(buf.String())
					}()
					t.Log("SUCCESS:", pod.ObjectMeta.Name, "-", pod.Status.ContainerStatuses[0].State.Terminated.Reason, "- LOG:", podLogString)
					break
				}
			}
		}
	}

	return successPod, errorPod, podLogString, nil
}

// SkipTestOnCI skips the test if running on CI
func SkipTestOnCI(t *testing.T) {
	ci := os.Getenv("CI")

	if ci == "true" {
		t.Skip("Failing on CI")
	}
}

func IsStringEmpty(data string) bool {
	if data == "" {
		return true
	} else {
		return false
	}
}

func IsErrorEmpty(err error) bool {
	if err == nil {
		return true
	} else {
		return false
	}
}

func IsBufferEmpty(buffer bytes.Buffer) bool {
	if buffer.String() == "" {
		return true
	} else {
		return false
	}
}

func AssessPodRequestAndLimit(ctx context.Context, client klient.Client, pod *v1.Pod) error {
	// Check if the pod has the "kata.peerpods.io/vm request and limit with value "1"

	podVmExtResource := "kata.peerpods.io/vm"

	request := pod.Spec.Containers[0].Resources.Requests[corev1.ResourceName(podVmExtResource)]
	limit := pod.Spec.Containers[0].Resources.Limits[corev1.ResourceName(podVmExtResource)]

	// Check if the request and limit are set to "1"
	if request.Cmp(resource.MustParse("1")) != 0 {
		return fmt.Errorf("request for podvm extended resource is not set to 1")
	}
	if limit.Cmp(resource.MustParse("1")) != 0 {
		return fmt.Errorf("limit for podvm extended resource is not set to 1")
	}

	return nil

}

func AssessPodTestCommands(ctx context.Context, client klient.Client, pod *v1.Pod, testCommands []TestCommand) (string, error) {
	var podlist v1.PodList
	if err := client.Resources(pod.Namespace).List(ctx, &podlist); err != nil {
		return "Failed to list pod", err
	}
	for _, testCommand := range testCommands {
		var stdout, stderr bytes.Buffer
		for _, podItem := range podlist.Items {
			if podItem.ObjectMeta.Name == pod.Name {
				//adding sleep time to intialize container and ready for Executing commands
				time.Sleep(5 * time.Second)
				if err := client.Resources(pod.Namespace).ExecInPod(ctx, pod.Namespace, pod.Name, testCommand.ContainerName, testCommand.Command, &stdout, &stderr); err != nil {
					if testCommand.TestErrorFn != nil {
						if !testCommand.TestErrorFn(err) {
							return err.Error(), fmt.Errorf("Command %v running in container %s produced unexpected output on error: %s", testCommand.Command, testCommand.ContainerName, err.Error())
						}
					} else {
						return err.Error(), err
					}
				} else if testCommand.TestErrorFn != nil {
					return "", fmt.Errorf("We expected an error from Pod %s, but it was not found", pod.Name)
				}
				if testCommand.TestCommandStderrFn != nil {
					if !testCommand.TestCommandStderrFn(stderr) {
						return stderr.String(), fmt.Errorf("Command %v running in container %s produced unexpected output on stderr: %s", testCommand.Command, testCommand.ContainerName, stderr.String())
					} else {
						return stderr.String(), nil
					}
				}
				if testCommand.TestCommandStdoutFn != nil {
					if !testCommand.TestCommandStdoutFn(stdout) {
						return stdout.String(), fmt.Errorf("Command %v running in container %s produced unexpected output on stdout: %s", testCommand.Command, testCommand.ContainerName, stdout.String())
					} else {
						return stdout.String(), nil
					}
				}
			}
		}
	}
	return "", nil
}

func ProvisionPod(ctx context.Context, client klient.Client, t *testing.T, pod *v1.Pod, podState v1.PodPhase, testCommands []TestCommand) error {
	if err := client.Resources().Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	if err := wait.For(conditions.New(client.Resources()).PodPhaseMatch(pod, podState), wait.WithTimeout(WAIT_POD_RUNNING_TIMEOUT)); err != nil {
		t.Fatal(err)
	}
	if podState == v1.PodRunning || len(testCommands) > 0 {
		t.Logf("Waiting for containers in pod: %v are ready", pod.Name)
		if err := wait.For(conditions.New(client.Resources()).ContainersReady(pod), wait.WithTimeout(WAIT_POD_RUNNING_TIMEOUT)); err != nil {
			//Added logs for debugging nightly tests
			clientset, err := kubernetes.NewForConfig(client.RESTConfig())
			if err != nil {
				t.Fatal(err)
			}
			actualPod, err := clientset.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("Expected Pod State: %v", podState)
			yamlData, err := yaml.Marshal(actualPod.Status)
			if err != nil {
				fmt.Println("Error marshaling pod.Status to YAML: ", err.Error())
			} else {
				t.Logf("Current Pod State: %v", string(yamlData))
			}
			if actualPod.Status.Phase == v1.PodRunning {
				fmt.Printf("Log of the pod %.v \n===================\n", actualPod.Name)
				podLogString, _ := GetPodLog(ctx, client, *actualPod)
				fmt.Println(podLogString)
				fmt.Printf("===================\n")
			}
			t.Fatal(err)
		}
	}
	return nil
}

func DeletePod(ctx context.Context, client klient.Client, pod *v1.Pod, tcDelDuration *time.Duration) error {
	duration := 1 * time.Minute
	if tcDelDuration == nil {
		tcDelDuration = &duration
	}
	if err := client.Resources().Delete(ctx, pod); err != nil {
		return err
	}
	if err := wait.For(conditions.New(
		client.Resources()).ResourceDeleted(pod),
		wait.WithInterval(5*time.Second),
		wait.WithTimeout(*tcDelDuration)); err != nil {
		return err
	}
	return nil
}

func CreateAndWaitForNamespace(ctx context.Context, client klient.Client, namespaceName string) error {
	log.Infof("Creating namespace '%s'...", namespaceName)
	nsObj := v1.Namespace{}
	nsObj.Name = namespaceName
	if err := client.Resources().Create(ctx, &nsObj); err != nil {
		return err
	}

	if err := waitForNamespaceToBeUseable(ctx, client, namespaceName); err != nil {
		return err
	}
	return nil
}

func waitForNamespaceToBeUseable(ctx context.Context, client klient.Client, namespaceName string) error {
	log.Infof("Wait for namespace '%s' be ready...", namespaceName)
	nsObj := v1.Namespace{}
	nsObj.Name = namespaceName
	if err := wait.For(conditions.New(client.Resources()).ResourceMatch(&nsObj, func(object k8s.Object) bool {
		ns, ok := object.(*v1.Namespace)
		if !ok {
			log.Printf("Not a namespace object: %v", object)
			return false
		}
		return ns.Status.Phase == v1.NamespaceActive
	}), wait.WithTimeout(WAIT_NAMESPACE_AVAILABLE_TIMEOUT)); err != nil {
		return err
	}

	// SH: There is a race condition where the default service account isn't ready when we
	// try and use it #1657, so we want to ensure that it is available before continuing.
	// As the serviceAccount doesn't have a status I can't seem to use the wait condition to
	// detect if it is ready, so do things the old-fashioned way
	log.Infof("Wait for default serviceaccount in namespace '%s'...", namespaceName)
	var saList v1.ServiceAccountList
	for start := time.Now(); time.Since(start) < WAIT_NAMESPACE_AVAILABLE_TIMEOUT; {
		if err := client.Resources(namespaceName).List(ctx, &saList); err != nil {
			return err
		}
		for _, sa := range saList.Items {
			if sa.ObjectMeta.Name == "default" {

				log.Infof("default serviceAccount exists, namespace '%s' is ready for use", namespaceName)
				return nil
			}
		}
		log.Tracef("default serviceAccount not found after %.0f seconds", time.Since(start).Seconds())
		time.Sleep(5 * time.Second)
	}
	return fmt.Errorf("default service account not found in namespace '%s' after %.0f seconds wait", namespaceName, WAIT_NAMESPACE_AVAILABLE_TIMEOUT.Seconds())
}

func DeleteAndWaitForNamespace(ctx context.Context, client klient.Client, namespaceName string) error {
	nsObj := v1.Namespace{}
	nsObj.Name = namespaceName
	if err := client.Resources().Delete(ctx, &nsObj); err != nil {
		return err
	}
	log.Infof("Deleting namespace '%s'...", nsObj.Name)
	if err := wait.For(conditions.New(
		client.Resources()).ResourceDeleted(&nsObj),
		wait.WithInterval(5*time.Second),
		wait.WithTimeout(60*time.Second)); err != nil {
		return err
	}
	log.Infof("Namespace '%s' has been successfully deleted within 60s", nsObj.Name)
	return nil
}

func AddImagePullSecretToDefaultServiceAccount(ctx context.Context, client klient.Client, secretName string) error {
	clientSet, err := kubernetes.NewForConfig(client.RESTConfig())
	if err != nil {
		return err
	}
	serviceAccount, err := clientSet.CoreV1().ServiceAccounts(E2eNamespace).Get(context.TODO(), "default", metav1.GetOptions{})
	if err != nil {
		return err
	}
	secretExists := false
	for _, secret := range serviceAccount.ImagePullSecrets {
		if secret.Name == secretName {
			secretExists = true
			break
		}
	}
	if !secretExists {
		// Update the ServiceAccount to add the imagePullSecret
		serviceAccount.ImagePullSecrets = append(serviceAccount.ImagePullSecrets, v1.LocalObjectReference{Name: secretName})
		_, err := clientSet.CoreV1().ServiceAccounts(E2eNamespace).Update(context.TODO(), serviceAccount, metav1.UpdateOptions{})
		if err != nil {
			return err
		}
		log.Infof("ServiceAccount %s updated successfully.", "default")
	}
	return nil
}

func GetPodNamesByLabel(ctx context.Context, client klient.Client, t *testing.T, namespace string, labelName string, labelValue string) (*v1.PodList, error) {

	clientset, err := kubernetes.NewForConfig(client.RESTConfig())
	if err != nil {
		t.Fatal(err)
		return nil, err
	}

	pods, err := clientset.CoreV1().Pods(namespace).List(context.TODO(), metav1.ListOptions{LabelSelector: labelName + "=" + labelValue})
	if err != nil {
		t.Fatal(err)
		return nil, err
	}

	return pods, nil
}

type podToString func(context.Context, klient.Client, v1.Pod) (string, error)

func getStringFromPod(ctx context.Context, client klient.Client, pod v1.Pod, fn podToString) (string, error) {
	var podlist v1.PodList
	if err := client.Resources(pod.Namespace).List(ctx, &podlist); err != nil {
		return "", err
	}
	for _, podItem := range podlist.Items {
		if podItem.ObjectMeta.Name == pod.Name {
			return fn(ctx, client, podItem)
		}
	}
	return "", fmt.Errorf("no pod matching %v, was found", pod)
}
