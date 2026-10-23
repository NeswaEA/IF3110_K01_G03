import { mountWidget } from "../app/mount";
import { ConnectionsPage } from "../features/connections/ConnectionsPage";

mountWidget("connections", () => <ConnectionsPage />);
