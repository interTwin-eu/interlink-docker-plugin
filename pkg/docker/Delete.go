package docker

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"

	exec "github.com/alexellis/go-execute/pkg/v1"
	"github.com/containerd/containerd/log"
	"github.com/intertwin-eu/interlink-docker-plugin/pkg/docker/dindmanager"
	v1 "k8s.io/api/core/v1"

	"path/filepath"
)

// DeleteHandler stops and deletes Docker containers from provided data
func (h *SidecarHandler) DeleteHandler(w http.ResponseWriter, r *http.Request) {
	log.G(h.Ctx).Info("\u23F3 [DELETE CALL] Received delete call from Interlink")
	var execReturn exec.ExecResult
	statusCode := http.StatusOK
	bodyBytes, err := io.ReadAll(r.Body)

	if err != nil {
		statusCode = http.StatusInternalServerError
		log.G(h.Ctx).Error(err)
		w.WriteHeader(statusCode)
		w.Write([]byte("Some errors occurred while deleting container. Check Docker Sidecar's logs"))
		return
	}

	var pod v1.Pod
	err = json.Unmarshal(bodyBytes, &pod)
	if err != nil {
		statusCode = http.StatusInternalServerError
		w.WriteHeader(statusCode)
		w.Write([]byte("Some errors occurred while creating container. Check Docker Sidecar's logs"))
		log.G(h.Ctx).Error(err)
		return
	}

	podUID := string(pod.UID)
	podNamespace := string(pod.Namespace)

	for _, container := range pod.Spec.Containers {
		containerName := podNamespace + "-" + podUID + "-" + container.Name
		h.GpuManager.Release(containerName)
	}

	log.G(h.Ctx).Debug("\u2705 [DELETE CALL] Deleting POD " + podUID + "_dind")

	cmd := []string{"rm", "-f", podUID + "_dind"}
	shell := exec.ExecTask{
		Command: "docker",
		Args:    cmd,
		Shell:   true,
	}
	execReturn, _ = shell.Execute()
	execReturn.Stdout = strings.ReplaceAll(execReturn.Stdout, "\n", "")

	if execReturn.Stderr != "" {
		log.G(h.Ctx).Error("\u274C [DELETE CALL] Error deleting container " + podUID + "_dind")
		statusCode = http.StatusInternalServerError
	} else {
		log.G(h.Ctx).Info("\u2705 [DELETE CALL] Deleted container " + podUID + "_dind")
	}

	// delete also the network of the docker dind container that is called string(data.Pod.UID) + "_dind_network"

	// retrieve the network ID from the dind manager

	dindSpec := dindmanager.DindSpecs{}
	dindSpec, err = h.DindManager.GetDindFromPodUID(podUID)

	// log the retrieved dindSpec
	log.G(h.Ctx).Info("\u2705 [DELETE CALL] Retrieved DindSpecs: " + dindSpec.DindID + " " + dindSpec.PodUID + " " + dindSpec.DindNetworkID + " ")

	cmd = []string{"network", "rm", dindSpec.DindNetworkID}
	shell = exec.ExecTask{
		Command: "docker",
		Args:    cmd,
		Shell:   true,
	}
	execReturn, _ = shell.Execute()
	execReturn.Stdout = strings.ReplaceAll(execReturn.Stdout, "\n", "")
	if execReturn.Stderr != "" {
		log.G(h.Ctx).Error("\u274C [DELETE CALL] Error deleting network " + dindSpec.DindNetworkID)
	} else {
		log.G(h.Ctx).Info("\u2705 [DELETE CALL] Deleted network " + dindSpec.DindNetworkID)
	}

	wd, err := os.Getwd()
	if err != nil {
		HandleErrorAndRemoveData(h, w, "Unable to get current working directory", err, "", "")
		return
	}
	podDirectoryPathToDelete := filepath.Join(wd, h.Config.DataRootFolder+"/"+podNamespace+"-"+podUID)
	log.G(h.Ctx).Info("\u2705 [DELETE CALL] Deleting directory " + podDirectoryPathToDelete)

	err = os.RemoveAll(podDirectoryPathToDelete)

	// set the dind available again
	err = h.DindManager.RemoveDindFromList(dindSpec.PodUID)
	if err != nil {
		log.G(h.Ctx).Error("\u274C [DELETE CALL] Error setting DIND container available")
	}

	w.WriteHeader(statusCode)
	if statusCode != http.StatusOK {
		w.Write([]byte("Some errors occurred deleting containers. Check Docker Sidecar's logs"))
	} else {
		w.Write([]byte("All containers for submitted Pods have been deleted"))
	}
}
