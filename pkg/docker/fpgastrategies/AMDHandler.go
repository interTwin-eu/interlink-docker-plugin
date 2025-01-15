package fpgastrategies

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"strings"

	"sync"

	"github.com/containerd/containerd/log"
)

type FPGASpecs struct {
	BDF         string
	Shell       string
	LogicUUID   string
	ContainerID string
	DeviceReady string
	Available   bool
	Index       int
}

type FPGAManager struct {
	FPGASpecsList  []FPGASpecs
	FPGASpecsMutex sync.Mutex
	Vendor         string
	Ctx            context.Context
}

type FPGAManagerInterface interface {
	Init() error
	Shutdown() error
	GetGPUSpecsList() []FPGASpecs
	Dump() error
	Discover() error
	Check() error
	GetAvailableGPUs(numGPUs int) ([]FPGASpecs, error)
	Assign(UUID string, containerID string) error
	Release(UUID string) error
	GetAndAssignAvailableGPUs(numGPUs int, containerID string) ([]FPGASpecs, error)
}

func (a *FPGAManager) Init() error {

	// Check if the Xilinx setup.sh file exists
	if _, err := os.Stat("/opt/xilinx/xrt/setup.sh"); os.IsNotExist(err) {
		return fmt.Errorf("/opt/xilinx/xrt/setup.sh does not exist: %v", err)
	}

	// Check if the path to Vitis exists
	if _, err := os.Stat("/tools/Xilinx/Vitis/2023.2"); os.IsNotExist(err) {
		return fmt.Errorf("/tools/Xilinx/Vitis/2023.2 does not exist: %v", err)
	}

	// Source the setup.sh to initialize Xilinx tools
	cmd := exec.Command("bash", "-c", "source /opt/xilinx/xrt/setup.sh")
	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("Error sourcing setup.sh: %v", err)
	}
	return nil
}

// Discover implements the Discover function of the FPGAManager interface
func (a *FPGAManager) Discover() error {

	cmd := exec.Command("lspci", "|", "grep", "Xilinx")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("Error running lspci command: %v", err)
	}

	// Extract BDF and other information from lspci output
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.Contains(line, "Xilinx") {
			// Parse the BDF and other necessary information from the lspci line
			// Example: "01:00.0 Processing accelerators: Xilinx Corporation Device 505c"
			parts := strings.Fields(line)
			if len(parts) < 3 {
				continue
			}

			bdf := parts[0]

			// Now, source the setup.sh and run `xbutil examine` to gather FPGA information
			cmd = exec.Command("bash", "-c", "source /opt/xilinx/xrt/setup.sh && xbutil examine")
			examineOutput, err := cmd.CombinedOutput()
			if err != nil {
				return fmt.Errorf("Error running xbutil examine: %v", err)
			}

			// Parse the xbutil examine output to extract FPGA details
			examineLines := strings.Split(string(examineOutput), "\n")
			for _, examineLine := range examineLines {
				// Find the line with the device details (look for "Devices present")
				if strings.Contains(examineLine, "Devices present") {
					// Parse the detailed information from the xbutil examine output
					// For example: "[0000:01:00.1]  :  xilinx_u55c_gen3x16_xdma_base_3  97088961-FEAE-DA91-52A2-1D9DFD63CCEF  user(inst=129)  Yes"
					fpgas := strings.Split(examineLine, "  :  ")
					if len(fpgas) > 1 {
						spec := FPGASpecs{
							BDF:       bdf,
							Shell:     fpgas[0],
							LogicUUID: fpgas[1],
							Available: true, // Assuming it's available for now, can update based on further output parsing
						}
						a.FPGASpecsList = append(a.FPGASpecsList, spec)
					}
				}
			}
		}
	}

	if len(a.FPGASpecsList) > 0 {
		log.G(a.Ctx).Info("\u2705 Discovered FPGAs:")
		for _, fpgaSpec := range a.FPGASpecsList {
			log.G(a.Ctx).Info(fmt.Sprintf("\u2705 BDF: %s, Shell: %s, LogicUUID: %s, Available: %t", fpgaSpec.BDF, fpgaSpec.Shell, fpgaSpec.LogicUUID, fpgaSpec.Available))
		}
	} else {
		log.G(a.Ctx).Info(" \u2705 No FPGAs discovered")
	}

	return nil
}

func (a *FPGAManager) Check() error {
	return nil
}

func (a *FPGAManager) Shutdown() error {

	return nil
}

func (a *FPGAManager) GetGPUSpecsList() []FPGASpecs {
	return a.FPGASpecsList
}

func (a *FPGAManager) Assign(UUID string, containerID string) error {

	for i := range a.FPGASpecsList {
		if a.FPGASpecsList[i].LogicUUID == UUID {

			if a.FPGASpecsList[i].Available == false {
				return fmt.Errorf("GPU with UUID %s is already in use by container %s", UUID, a.FPGASpecsList[i].ContainerID)
			}

			a.FPGASpecsList[i].ContainerID = containerID
			a.FPGASpecsList[i].Available = false
			break
		}
	}
	return nil

}

func (a *FPGAManager) Release(containerID string) error {

	a.FPGASpecsMutex.Lock()
	defer a.FPGASpecsMutex.Unlock()

	for i := range a.FPGASpecsList {
		if a.FPGASpecsList[i].ContainerID == containerID {

			if a.FPGASpecsList[i].Available == true {
				continue
			}

			a.FPGASpecsList[i].ContainerID = ""
			a.FPGASpecsList[i].Available = true
		}
	}

	return nil
}

func (a *FPGAManager) GetAvailableGPUs(numGPUs int) ([]FPGASpecs, error) {

	var availableGPUs []FPGASpecs
	for _, gpuSpec := range a.FPGASpecsList {
		if gpuSpec.Available == true {
			availableGPUs = append(availableGPUs, gpuSpec)
			if len(availableGPUs) == numGPUs {
				return availableGPUs, nil
			}
		}
	}
	return nil, fmt.Errorf("Not enough available GPUs. Requested: %d, Available: %d", numGPUs, len(availableGPUs))
}

func (a *FPGAManager) GetAndAssignAvailableGPUs(numGPUs int, containerID string) ([]FPGASpecs, error) {

	a.FPGASpecsMutex.Lock()
	defer a.FPGASpecsMutex.Unlock()

	fpgaSpecs, err := a.GetAvailableGPUs(numGPUs)
	if err != nil {
		return nil, err
	}

	for _, fpgaSpec := range fpgaSpecs {
		err = a.Assign(fpgaSpec.LogicUUID, containerID)
		if err != nil {
			return nil, err
		}
	}

	return fpgaSpecs, nil
}

// dump the GPUSpecsList into a JSON file
func (a *FPGAManager) Dump() error {

	// Convert the array to JSON format
	jsonData, err := json.MarshalIndent(a.FPGASpecsList, "", "  ")
	if err != nil {
		return fmt.Errorf("Error marshalling JSON: %v", err)
	}

	// Write JSON data to a file
	err = ioutil.WriteFile("gpu_specs.json", jsonData, 0644)
	if err != nil {
		return fmt.Errorf("Error writing to file: %v", err)
	}

	return nil
}
