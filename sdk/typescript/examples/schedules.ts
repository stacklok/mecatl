import type { ListSchedulesRequest } from "@stacklok/mecatl-sdk/gen";
import { connect } from "@stacklok/mecatl-sdk/node";

await using client = connect({
  baseUrl: process.env.MECATL_URL ?? "http://127.0.0.1:8080",
});

const request: ListSchedulesRequest = { $typeName: "mecatl.v1.ListSchedulesRequest" };
const schedules = await client.schedules.list(request);

for (const schedule of schedules.schedules) {
  console.log(schedule.spec?.name, schedule.state?.nextFireAt);
}
