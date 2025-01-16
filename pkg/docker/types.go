package docker

type DockerRunStruct struct {
	Name            string `json:"name"`
	Command         string `json:"command"`
	IsInitContainer bool   `json:"isInitContainer"`
	GpuArgs         string `json:"gpuArgs"`
	FpgaArgs        string `json:"fpgaArgs"`
}
type CreateStruct struct {
	PodUID string `json:"PodUID"`
	PodJID string `json:"PodJID"`
}
