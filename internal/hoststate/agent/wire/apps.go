package wire

const AppsPathPrefix = "/v1/apps/"

type AppIdentityDocument struct {
	Username string `json:"username"`
	UID      int    `json:"uid"`
	GID      int    `json:"gid"`
}

type AppPreparationDocument struct {
	Folders []string `json:"folders"`
	Shared  bool     `json:"shared"`
}
