import { query, type SankeyData } from "../lib/api";
import { bytes } from "../lib/format";
import { navigate, useFetch } from "../lib/hooks";
import { historical, useView } from "../lib/view";
import { Card, Empty, ErrorNote, PageHead, RangeControl, ZoneControl } from "../components/Controls";
import { Sankey } from "../components/Sankey";

export function FlowMap() {
  const { range, zone } = useView();
  const r = historical(range);
  const res = useFetch<SankeyData>(`/api/v1/sankey${query({ range: r, zone })}`, 60_000);
  const data = res.data;
  return (
    <div className="page">
      <PageHead title="Flow map" sub="Which devices use which services, and where the traffic goes">
        <ZoneControl />
        <RangeControl />
      </PageHead>
      {res.error && <ErrorNote message={res.error} />}
      <Card stale={res.stale} note={data && data.total > 0 ? `${bytes(data.total)} in total. Ribbon thickness is volume; hover for numbers, click a node to see its connections.` : undefined}>
        {data && data.nodes.length > 0 ? (
          <Sankey data={data} onSelect={(n) => {
            if (n.col === 0) navigate(`/devices/${n.ref}`);
            else navigate("/flows", { tab: "history", [n.col === 1 ? "service" : "dest"]: n.ref, label: n.label });
          }} />
        ) : data ? <Empty>No traffic recorded in this period.</Empty> : <Empty>Loading…</Empty>}
      </Card>
      <p className="foot-note">The heaviest devices, services and destinations are shown individually; the rest are combined into "Other". Destinations are grouped by domain when the router resolved a name, otherwise by the organisation that owns the address.</p>
    </div>
  );
}
