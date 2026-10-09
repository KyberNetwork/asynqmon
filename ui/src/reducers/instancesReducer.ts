import {
  SELECT_INSTANCE,
  InstancesActionTypes,
} from "../actions/instancesActions";

export interface InstancesState {
  // List of redis instances available to monitor.
  available: RedisInstance[];

  // Id of the currently selected instance.
  selectedId: string;
}

// The list of instances is injected by the server under window.INSTANCES.
// Fall back to a single default instance for safety.
const availableInstances: RedisInstance[] =
  window.INSTANCES && window.INSTANCES.length > 0
    ? window.INSTANCES
    : [{ id: "default", name: "default" }];

export const initialState: InstancesState = {
  available: availableInstances,
  selectedId: availableInstances[0].id,
};

function instancesReducer(
  state = initialState,
  action: InstancesActionTypes
): InstancesState {
  switch (action.type) {
    case SELECT_INSTANCE:
      return {
        ...state,
        selectedId: action.id,
      };

    default:
      return state;
  }
}

export default instancesReducer;
