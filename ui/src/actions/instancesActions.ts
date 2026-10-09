import { setCurrentInstanceId } from "../api";

// List of instance related action types.
export const SELECT_INSTANCE = "SELECT_INSTANCE";

interface SelectInstanceAction {
  type: typeof SELECT_INSTANCE;
  id: string; // id of the newly selected redis instance
}

// Union of all instance related action types.
export type InstancesActionTypes = SelectInstanceAction;

// selectInstance changes the currently active redis instance.
// It also updates the api module so subsequent requests are scoped to it.
export function selectInstance(id: string): SelectInstanceAction {
  setCurrentInstanceId(id);
  return {
    type: SELECT_INSTANCE,
    id,
  };
}
