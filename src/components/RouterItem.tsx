import React from 'react';
import styled from 'styled-components';
import Button from './common/Button';
import { NDKEvent } from '@nostr-dev-kit/ndk';
import ReleaseSelector from './ReleaseSelector';
import { ScanResult } from '../../shared/types';

// Types
interface RouterItemProps {
  router: ScanResult;
  releases: NDKEvent[];
  selectedReleaseId?: string;
  onReleaseSelect: (routerIp: string, release: NDKEvent) => void;
  onConnect: (routerIp: string, releaseId?: string) => void;
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

const RouterDetailsList = styled.div`
  margin-top: 0.5rem;
  border-top: 1px solid ${props => props.theme.colors.border || '#eee'};
  padding-top: 0.5rem;
  font-size: ${props => props.theme.fontSizes.sm};
`;

const RouterDetailItem = styled.div`
  display: flex;
  margin-bottom: 0.25rem;
  
  &:last-child {
    margin-bottom: 0;
  }
`;

const DetailLabel = styled.div`
  font-weight: ${props => props.theme.fontWeights.medium};
  width: 100px;
  flex-shrink: 0;
`;

const DetailValue = styled.div`
  color: ${props => props.theme.colors.textSecondary};
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
}) => {
  console.log('RouterItem props:', {
    ip: router.ip,
    sshOpen: router.sshOpen,
    meta: router.meta
  });
  
  return (
    <RouterItemContainer>
      <RouterHeader>
        <RouterInfo>
          <RouterIP>{router.ip}</RouterIP>
          <RouterDetail>
            {router.meta?.isGateway ? 'Default Gateway' : 'Router'}
            {router.meta?.isOpenwrt && ' (OpenWrt)'}
            {router.meta?.status === 'no-ssh' && ' (SSH Not Active)'}
            {router.meta?.isOpenwrt && router.meta.boardInfo?.board_name &&
              ` - ${router.meta.boardInfo.board_name}`}
          </RouterDetail>
          
          {router.meta?.isOpenwrt && router.meta.boardInfo && router.meta.boardInfo.release && (
            <RouterDetailsList>
              <RouterDetailItem>
                <DetailLabel>Distribution:</DetailLabel>
                <DetailValue>{router.meta.boardInfo.release.distribution || 'Unknown'}</DetailValue>
              </RouterDetailItem>
              <RouterDetailItem>
                <DetailLabel>Architecture:</DetailLabel>
                <DetailValue>{router.meta.boardInfo.release.architecture || 'Unknown'}</DetailValue>
              </RouterDetailItem>
            </RouterDetailsList>
          )}
        </RouterInfo>
        <RouterActions>
          <ReleaseSelector
            releases={releases}
            routerBoardName={router.meta?.boardInfo?.board_name}
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
            Install
          </Button>
        </RouterActions>
      </RouterHeader>
    </RouterItemContainer>
  );
};

export default RouterItem;