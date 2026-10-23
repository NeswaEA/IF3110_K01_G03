import { mountWidget } from "../app/mount";
import { DatabaseViewer } from "../features/viewer/DatabaseViewer";

mountWidget("database-viewer", (el) => <DatabaseViewer databaseId={el.dataset.databaseId ?? ""} />);
