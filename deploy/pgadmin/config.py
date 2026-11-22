# pgAdmin settings, loaded through CONFIG_DISTRO_FILE_PATH by
# deploy/bin/run-pgadmin.sh. DATA_DIR comes from the environment.
import os

DATA_DIR = os.environ["PGADMIN_DATA_DIR"]

# Desktop mode (PGADMIN_SERVER_MODE=OFF): no login of pgAdmin's own. Caddy's
# check of ROAMIND_DASHBOARD_USERS is the gate, as for the notebook.
MASTER_PASSWORD_REQUIRED = False
DEFAULT_SERVER = "127.0.0.1"
DEFAULT_SERVER_PORT = 5050

# Served by Caddy over HTTPS under the tailnet name.
SESSION_COOKIE_SECURE = True
CHECK_SUPPORTED_BROWSER = False
