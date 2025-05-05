import React from 'react';
import styled from 'styled-components';
import Button from './common/Button';
import { NDKEvent } from '@nostr-dev-kit/ndk';
import ReleaseSelector from './ReleaseSelector';

// Types
interface RouterItemProps {
  router: { ip: string; sshOpen: boolean; meta?: any };
  releases: NDKEvent[];
  selectedReleaseId?: string;
  onReleaseSelect: (routerIp: string, release: NDKEvent) => void;
  onConnect: (routerIp: string, releaseId?: string) => void;
  boardName?: string;
}

// Styled components
const RouterItemContainer = styled.div`
  position: relative;
  display: flex;
  flex-direction: column;
  padding: 1rem;
  border: 1px solid ${props => props.theme.colors.border};
  border-radius: ${props => props.theme.radii.md};
  margin-bottom: 0.75rem;
  transition: all ${props => props.theme.transitions.fast};
  
  &:hover {
    border-color: ${props => props.theme.colors.primary};
    box-shadow: ${props => props.theme.shadows.sm};
  }
`;

const RouterHeader = styled.div`
  display: flex;
  align-items: center;
  justify-content: space-between;
  width: 100%;
`;

const RouterInfo = styled.div`
  flex: 1;
`;

const RouterIP = styled.div`
  font-weight: ${props => props.theme.fontWeights.medium};
  font-size: ${props => props.theme.fontSizes.md};
`;

const RouterDetail = styled.div`
  font-size: ${props => props.theme.fontSizes.sm};
  color: ${props => props.theme.colors.textSecondary};
  margin-top: 0.25rem;
`;

const RouterActions = styled.div`
  display: flex;
  gap: 0.5rem;
`;

// Component
const RouterItem: React.FC<RouterItemProps> = ({
  router,
  releases,
  selectedReleaseId,
  onReleaseSelect,
  onConnect,
  boardName
}) => {
  return (
    <RouterItemContainer>
      <RouterHeader>
        <RouterInfo>
          <RouterIP>{router.ip}</RouterIP>
          <RouterDetail>
            {router.meta?.isGateway ? 'Default Gateway' : 'OpenWRT Router'}
            {router.meta?.status === 'no-ssh' && ' (SSH Not Active)'}
            {boardName && ` - ${boardName}`}
          </RouterDetail>
        </RouterInfo>
        <RouterActions>
          <ReleaseSelector
            releases={releases}
            routerBoardName={boardName}
            selectedReleaseId={selectedReleaseId}
            onReleaseSelect={(release) => onReleaseSelect(router.ip, release)}
            buttonLabel="Select Release"
            disabled={router.meta?.status === 'no-ssh'}
          />
          <Button
            variant="primary"
            size="small"
            onClick={() => onConnect(router.ip, selectedReleaseId)}
            disabled={router.meta?.status === 'no-ssh' || !selectedReleaseId}
          >
            Connect
          </Button>
        </RouterActions>
      </RouterHeader>
    </RouterItemContainer>
  );
};

export default RouterItem;