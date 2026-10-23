import { mountWidget } from "../app/mount";
import { DatabaseList } from "../features/viewer/DatabaseList";

mountWidget("database-list", () => <DatabaseList />);
