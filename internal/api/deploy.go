package api

import (
	"net/http"

	"github.com/opsramp/opamp-orchestrator/internal/deploy"
	"github.com/opsramp/opamp-orchestrator/internal/model"
)

// handleDeployStart kicks off a bulk OpsRamp-agent installation over SSH. The
// request carries SSH credentials which are used only in-memory and never
// persisted. Deployment always runs the OpsRamp installer (deployAgent.sh) with
// the connector's credentials — there is no arbitrary-command path.
func (s *Server) handleDeployStart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Action         string `json:"action"` // install|preflight|repair|upgrade|uninstall
		Targets        string `json:"targets"`
		SSHUser        string `json:"ssh_user"`
		SSHPassword    string `json:"ssh_password"`
		SSHPrivateKey  string `json:"ssh_private_key"`
		SSHKeyPassword string `json:"ssh_key_passphrase"`
		Port           int    `json:"port"`
		UseSudo        bool   `json:"use_sudo"`
		IntegrationID  string `json:"integration_id"`
		EnableLogMgmt  bool   `json:"enable_log_mgmt"`
		AgentKey       string `json:"agent_key"`
		AgentSecret    string `json:"agent_secret"`

		// Optional jump host.
		BastionHost       string `json:"bastion_host"`
		BastionUser       string `json:"bastion_user"`
		BastionPassword   string `json:"bastion_password"`
		BastionPrivateKey string `json:"bastion_private_key"`
		BastionPassphrase string `json:"bastion_key_passphrase"`
		BastionPort       int    `json:"bastion_port"`

		// Uninstall/decommission options.
		UninstallCommand string `json:"uninstall_command"`
		Deregister       bool   `json:"deregister"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	job, err := s.deploy.StartJob(r.Context(), deploy.StartRequest{
		Action:     body.Action,
		TargetSpec: body.Targets,
		Creds: deploy.Credentials{
			User:              body.SSHUser,
			Password:          body.SSHPassword,
			PrivateKey:        body.SSHPrivateKey,
			Passphrase:        body.SSHKeyPassword,
			Port:              body.Port,
			UseSudo:           body.UseSudo,
			BastionHost:       body.BastionHost,
			BastionUser:       body.BastionUser,
			BastionPassword:   body.BastionPassword,
			BastionPrivateKey: body.BastionPrivateKey,
			BastionPassphrase: body.BastionPassphrase,
			BastionPort:       body.BastionPort,
		},
		IntegrationID:    body.IntegrationID,
		EnableLogMgmt:    body.EnableLogMgmt,
		AgentKey:         body.AgentKey,
		AgentSecret:      body.AgentSecret,
		UninstallCommand: body.UninstallCommand,
		Deregister:       body.Deregister,
		CreatedBy:        actor(r),
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

// handleReconcile returns the current fleet reconciliation report: version
// drift, down agents, and recommended remediations.
func (s *Server) handleReconcile(w http.ResponseWriter, r *http.Request) {
	rep, err := s.reconcile.Evaluate(r.Context())
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (s *Server) handleDeployList(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.store.ListDeployJobs(r.Context(), 50)
	if mapStoreErr(w, err) {
		return
	}
	if jobs == nil {
		jobs = []model.DeployJob{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
}

func (s *Server) handleDeployGet(w http.ResponseWriter, r *http.Request) {
	job, err := s.store.GetDeployJob(r.Context(), r.PathValue("id"))
	if mapStoreErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, job)
}
