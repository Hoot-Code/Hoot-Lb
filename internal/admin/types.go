package admin

type ListPoolsResponse struct {
	Pools []PoolInfo `json:"pools"`
}

type PoolInfo struct {
	Name      string        `json:"name"`
	Algorithm string        `json:"algorithm"`
	Discovery bool          `json:"discovery"`
	Backends  []BackendInfo `json:"backends"`
}

type BackendInfo struct {
	Address  string `json:"address"`
	Weight   int    `json:"weight"`
	Healthy  bool   `json:"healthy"`
	Draining bool   `json:"draining"`
}

type AddBackendRequest struct {
	Address string `json:"address"`
	Weight  int    `json:"weight"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}

type RestartResponse struct {
	Status string `json:"status"`
}
