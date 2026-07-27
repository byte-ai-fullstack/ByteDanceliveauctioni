# Source Revisions

This public monorepo is assembled from sanitized, tracked snapshots of three component repositories. Component Git histories are not imported, which prevents previously committed local configuration from being copied into the public repository.

| Component | Source repository | Exported source HEAD | Public mirror rule |
| --- | --- | --- | --- |
| Backend | `Ye-yellow/auction-backend` | `427c178fd35a` | Tracked HEAD only; `.git`, ignored/untracked files, local environments, build output, test output, and internal agent notes excluded. |
| PC merchant console | `Ye-yellow/auction-frontend` | `8b67f61d6b27` | Tracked HEAD only; `.git`, dependencies, build output, local environments, and internal agent notes excluded. |
| H5 buyer client | `Ye-yellow/aution-h5` | `0ce3d413a059` | Tracked HEAD only; `.git`, dependencies, build output, captured fixture datasets, unfinished uncommitted drafts, and internal agent notes excluded. |

The delivery branch is based on the existing public `main` history and replaces each component directory with the corresponding sanitized snapshot. Root-level public documentation and CI are maintained directly in this repository.
