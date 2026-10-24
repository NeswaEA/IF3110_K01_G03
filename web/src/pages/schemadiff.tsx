import { mountWidget } from "../app/mount";
import { SchemaDiffPage, type DiffInitialState } from "../features/schemadiff/SchemaDiffPage";

mountWidget("schema-diff", (el) => <SchemaDiffPage initial={JSON.parse(el.dataset.initial!) as DiffInitialState} />, "notify");
