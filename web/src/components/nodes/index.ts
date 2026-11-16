import AgentNode from './AgentNode.vue';
import ToolNode from './ToolNode.vue';
import SkillNode from './SkillNode.vue';
import OrchestratorNode from './OrchestratorNode.vue';
import DecisionNode from './DecisionNode.vue';
import RetrievalNode from './RetrievalNode.vue';
import GroupNode from './GroupNode.vue';

export const nodeTypes = {
  agent: AgentNode,
  tool: ToolNode,
  skill: SkillNode,
  orchestrator: OrchestratorNode,
  group: GroupNode,
  decision: DecisionNode,
  retrieval: RetrievalNode,
};

export { AgentNode, ToolNode, SkillNode, OrchestratorNode, GroupNode, RetrievalNode };
