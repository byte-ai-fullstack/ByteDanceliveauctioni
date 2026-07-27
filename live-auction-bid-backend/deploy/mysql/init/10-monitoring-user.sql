-- Local/CI infrastructure only. Production creates this principal through the secret manager and DBA workflow.
CREATE USER IF NOT EXISTS 'exporter'@'%' IDENTIFIED BY 'exporter_local_only' WITH MAX_USER_CONNECTIONS 3;
GRANT PROCESS, REPLICATION CLIENT, SELECT ON *.* TO 'exporter'@'%';
